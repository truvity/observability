package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// requestBodyLimit bounds how much of a POST this service will read. A
// public endpoint with no cap on request size is a denial-of-service
// surface for free; nothing this service is meant to receive is anywhere
// near this large.
const requestBodyLimit = 1 << 20

// Server is the whole of what this service does with a message: verify,
// confirm, match, render, post, count. See docs/alert-ingress.md for the
// order and the reasoning behind each step.
type Server struct {
	Config       Config
	Verifier     *Verifier
	Alertmanager *AlertmanagerClient
	Metrics      *Metrics
	Logger       *slog.Logger
}

func (s *Server) topicAllowed(topic string) bool {
	for _, t := range s.Config.Topics {
		if t == topic {
			return true
		}
	}

	return false
}

// verdict is what the shared pipeline decided about one message.
type verdict int

const (
	// verdictOK: handled — an alert was posted, the heartbeat counted, or
	// a subscription confirmed.
	verdictOK verdict = iota
	// verdictIgnored: valid and allow-listed, but deliberately not acted
	// on (a confirmation message arriving over a queue). Nothing to retry.
	verdictIgnored
	// verdictRejected: refused; retrying cannot help.
	verdictRejected
	// verdictFailed: valid, but Alertmanager did not accept the alert;
	// retrying later can help.
	verdictFailed
)

// result is the pipeline's answer. Rejections carry the closed `reason`
// label and a human detail; neither ever contains the message body.
type result struct {
	verdict verdict
	topic   string
	reason  string
	detail  string
}

func rejected(topic, reason, detail string) result {
	return result{verdict: verdictRejected, topic: topic, reason: reason, detail: detail}
}

// ServeHTTP is the HTTP input: read, parse, run the shared pipeline, and
// answer. Every rejection goes through reject, which is the only place a
// 403 and a `rejected` count are produced together — so the two can never
// drift apart.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, requestBodyLimit))
	if err != nil {
		s.reject(w, "", ReasonMalformed, fmt.Sprintf("reading request body: %s", err))
		return
	}

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		s.reject(w, "", ReasonMalformed, fmt.Sprintf("request body is not the envelope this service expects: %s", err))
		return
	}

	res := s.Handle(env, true)

	switch res.verdict {
	case verdictRejected:
		s.reject(w, res.topic, res.reason, res.detail)
	case verdictFailed:
		http.Error(w, "posting to alertmanager failed", http.StatusBadGateway)
	default:
		w.WriteHeader(http.StatusOK)
	}
}

// Handle runs the steps in order for one parsed envelope, whatever input
// it arrived on (HTTP delivery or queue message): verify the signature,
// check the topic allow-list, then confirm / count the heartbeat / match
// and post. It counts the successful outcomes itself; counting a
// rejection is the caller's, through countRejected, so each input answers
// in its own way.
//
// confirm is true for HTTP, where the provider expects a
// SubscriptionConfirmation to be followed, and for the queue input when
// input.sqs.confirmSubscriptions is on (a cross-account subscription made
// by the topic owner stays pending until the queue owner confirms). It is
// safe there because the signature and the allow-list are checked first and
// Verifier.Confirm pins the SubscribeURL host. With confirm false a
// confirmation message is logged and ignored, never followed.
func (s *Server) Handle(env Envelope, confirm bool) result {
	// 1. Verify the signature. Everything below trusts the envelope's
	// own fields, so nothing above this line may.
	if err := s.Verifier.Verify(env); err != nil {
		return rejected(env.TopicArn, ReasonSignature, fmt.Sprintf("signature: %s", err))
	}

	// 2. Confirm only an allow-listed topic. An endpoint that confirms
	// anything can be subscribed to anyone's topic and fed alerts.
	if !s.topicAllowed(env.TopicArn) {
		return rejected(env.TopicArn, ReasonTopic, fmt.Sprintf("topic %q is not on the allow-list", env.TopicArn))
	}

	switch env.Type {
	case "SubscriptionConfirmation", "UnsubscribeConfirmation":
		if !confirm {
			s.Logger.Info("ignoring a confirmation message received over the queue; queue subscriptions need none",
				"type", env.Type, "topic", env.TopicArn)
			s.Metrics.Messages.WithLabelValues("ignored", "confirmation").Inc()

			return result{verdict: verdictIgnored, topic: env.TopicArn}
		}

		if env.Type != "SubscriptionConfirmation" {
			// UnsubscribeConfirmation: accepting it silently would mean a
			// topic could stop delivering, through no fault of this
			// service, with nothing anywhere saying so. Refusing it loudly
			// is what makes that visible instead.
			return rejected(env.TopicArn, ReasonType, fmt.Sprintf("message Type %q is neither Notification nor SubscriptionConfirmation", env.Type))
		}

		if err := s.Verifier.Confirm(env); err != nil {
			return rejected(env.TopicArn, ReasonConfirmation, fmt.Sprintf("confirmation: %s", err))
		}

		s.Metrics.Messages.WithLabelValues("received", "subscribe").Inc()

		return result{verdict: verdictOK, topic: env.TopicArn}
	case "Notification":
		// Falls through to matching below.
	default:
		// A signed, allow-listed message of a Type this service does not
		// otherwise handle.
		return rejected(env.TopicArn, ReasonType, fmt.Sprintf("message Type %q is neither Notification nor SubscriptionConfirmation", env.Type))
	}

	// Mappings and templates see the parsed body plus the signed envelope
	// fields under `_sns`; see envelopeKey.
	body := buildInput(env)

	// The heartbeat is recognised before the ordinary mapping rules, and
	// is never turned into an alert — see Heartbeat's own comment in
	// config.go for why.
	if len(s.Config.Heartbeat.Match) > 0 && matches(s.Config.Heartbeat.Match, body) {
		s.Metrics.Messages.WithLabelValues("received", "heartbeat").Inc()

		return result{verdict: verdictOK, topic: env.TopicArn}
	}

	// 3. Match against the mapping rules, in order; the first hit wins.
	for _, m := range s.Config.Mappings {
		if matchesMapping(m, body) {
			return s.post(m, body, env.Message, env.TopicArn)
		}
	}

	// 4. Unmapped is still an alert. A message no rule matches becomes
	// CloudEventUnmapped rather than being dropped: a drop is the
	// failure mode this whole repository exists to close.
	labels, annotations := unmappedAlert(s.Config.Unmapped, env.Message)

	return s.deliver(env.TopicArn, "unmapped", "unmapped", labels, annotations, false, 0)
}

// post renders spec against body and delivers it. If rendering fails —
// an operator's own template has a mistake in it — this falls back to
// CloudEventUnmapped rather than dropping the message: a broken mapping
// is exactly the situation the unmapped path exists to catch, and it is
// far better for an on-call person to see "unmapped" and go fix the
// template than for the message to vanish while everything reports
// healthy.
func (s *Server) post(m Mapping, body map[string]any, rawMessage, topic string) result {
	mappingName, outcome := m.Name, "mapped"

	labels, annotations, err := renderAlert(m.Alert, body)
	resolved := false

	if err == nil {
		resolved, err = renderResolved(m.Alert, body)
	}

	if err != nil {
		s.Logger.Error("rendering mapping template; falling back to CloudEventUnmapped rather than dropping the message",
			"mapping", mappingName, "error", err)

		labels, annotations = unmappedAlert(s.Config.Unmapped, rawMessage)
		mappingName, outcome = "unmapped", "unmapped"
		resolved, m.ResolveAfter = false, 0
	}

	return s.deliver(topic, mappingName, outcome, labels, annotations, resolved, time.Duration(m.ResolveAfter))
}

// deliver POSTs one alert to Alertmanager and reports the outcome. A firing
// alert resolves after resolveAfter (Config.ResolveAfter when that is
// zero); a resolved one is posted already ended, which is how Alertmanager
// is told to clear the alert with the same labels. A delivery failure is
// logged and answered with a 5xx rather than counted as this outcome: the
// provider's own retry is what recovers a transient Alertmanager outage,
// and the message is counted only once that retry actually lands.
func (s *Server) deliver(topic, mappingName, outcome string, labels, annotations map[string]string, resolved bool, resolveAfter time.Duration) result {
	if resolveAfter == 0 {
		resolveAfter = time.Duration(s.Config.ResolveAfter)
	}

	now := time.Now().UTC()
	alert := AlertmanagerAlert{
		Labels:      labels,
		Annotations: annotations,
		StartsAt:    now,
		EndsAt:      now.Add(resolveAfter),
	}

	if resolved {
		alert.EndsAt = now
	}

	if err := s.Alertmanager.Post(alert); err != nil {
		s.Logger.Error("posting to alertmanager", "mapping", mappingName, "error", err)

		return result{verdict: verdictFailed, topic: topic}
	}

	s.Metrics.Messages.WithLabelValues(outcome, mappingName).Inc()

	return result{verdict: verdictOK, topic: topic}
}

// reject answers 403 and counts the message rejected. Used for a
// signature failure, an off-allow-list topic, a failed confirmation, and
// a message Type this service does not otherwise handle — every one of
// them a shape an attacker who can merely reach the public route could
// produce, and every one of them counted rather than merely logged: a
// spike here is the thing that should be noticed.
func (s *Server) reject(w http.ResponseWriter, topic, reasonLabel, reason string) {
	s.countRejected(topic, reasonLabel, reason)
	http.Error(w, "refused", http.StatusForbidden)
}

// countRejected logs and counts one rejection; the shared half of reject,
// also used by the queue input, which deletes the message instead of
// answering 403.
func (s *Server) countRejected(topic, reasonLabel, reason string) {
	s.Logger.Warn("rejected", "topic", topic, "reason", reason)
	s.Metrics.Messages.WithLabelValues("rejected", "").Inc()
	s.Metrics.Rejected.WithLabelValues(reasonLabel).Inc()
}
