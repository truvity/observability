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

// ServeHTTP implements the six steps in order. Every early return before
// step 6 goes through reject, which is the only place a 403 and a
// `rejected` count are produced together — so the two can never drift
// apart.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, requestBodyLimit))
	if err != nil {
		s.reject(w, "", fmt.Sprintf("reading request body: %s", err))
		return
	}

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		s.reject(w, "", fmt.Sprintf("request body is not the envelope this service expects: %s", err))
		return
	}

	// 1. Verify the signature. Everything below trusts the envelope's
	// own fields, so nothing above this line may.
	if err := s.Verifier.Verify(env); err != nil {
		s.reject(w, env.TopicArn, fmt.Sprintf("signature: %s", err))
		return
	}

	// 2. Confirm only an allow-listed topic. An endpoint that confirms
	// anything can be subscribed to anyone's topic and fed alerts.
	if !s.topicAllowed(env.TopicArn) {
		s.reject(w, env.TopicArn, fmt.Sprintf("topic %q is not on the allow-list", env.TopicArn))
		return
	}

	switch env.Type {
	case "SubscriptionConfirmation":
		if err := s.Verifier.Confirm(env); err != nil {
			s.reject(w, env.TopicArn, fmt.Sprintf("confirmation: %s", err))
			return
		}

		s.Metrics.Messages.WithLabelValues("received", "subscribe").Inc()
		w.WriteHeader(http.StatusOK)

		return
	case "Notification":
		// Falls through to matching below.
	default:
		// A signed, allow-listed message of a Type this service does not
		// otherwise handle. UnsubscribeConfirmation is the one real
		// example: accepting it silently would mean a topic could stop
		// delivering, through no fault of this service, with nothing
		// anywhere saying so. Refusing it loudly is what makes that
		// visible instead.
		s.reject(w, env.TopicArn, fmt.Sprintf("message Type %q is neither Notification nor SubscriptionConfirmation", env.Type))
		return
	}

	body := parseBody(env.Message)

	// The heartbeat is recognised before the ordinary mapping rules, and
	// is never turned into an alert — see Heartbeat's own comment in
	// config.go for why.
	if len(s.Config.Heartbeat.Match) > 0 && matches(s.Config.Heartbeat.Match, body) {
		s.Metrics.Messages.WithLabelValues("received", "heartbeat").Inc()
		w.WriteHeader(http.StatusOK)

		return
	}

	// 3. Match against the mapping rules, in order; the first hit wins.
	for _, m := range s.Config.Mappings {
		if matches(m.Match, body) {
			s.post(w, m.Name, "mapped", m.Alert, body, env.Message)
			return
		}
	}

	// 4. Unmapped is still an alert. A message no rule matches becomes
	// CloudEventUnmapped rather than being dropped: a drop is the
	// failure mode this whole repository exists to close.
	labels, annotations := unmappedAlert(env.Message)
	s.deliver(w, "unmapped", "unmapped", labels, annotations)
}

// post renders spec against body and delivers it. If rendering fails —
// an operator's own template has a mistake in it — this falls back to
// CloudEventUnmapped rather than dropping the message: a broken mapping
// is exactly the situation the unmapped path exists to catch, and it is
// far better for an on-call person to see "unmapped" and go fix the
// template than for the message to vanish while everything reports
// healthy.
func (s *Server) post(w http.ResponseWriter, mappingName, outcome string, spec AlertSpec, body map[string]any, rawMessage string) {
	labels, annotations, err := renderAlert(spec, body)
	if err != nil {
		s.Logger.Error("rendering mapping template; falling back to CloudEventUnmapped rather than dropping the message",
			"mapping", mappingName, "error", err)

		labels, annotations = unmappedAlert(rawMessage)
		mappingName, outcome = "unmapped", "unmapped"
	}

	s.deliver(w, mappingName, outcome, labels, annotations)
}

// deliver POSTs one alert to Alertmanager, resolving after
// Config.ResolveAfter, and answers the request. A delivery failure is
// logged and answered with a 5xx rather than counted as this outcome: the
// provider's own retry is what recovers a transient Alertmanager outage,
// and the message is counted only once that retry actually lands.
func (s *Server) deliver(w http.ResponseWriter, mappingName, outcome string, labels, annotations map[string]string) {
	now := time.Now().UTC()
	alert := AlertmanagerAlert{
		Labels:      labels,
		Annotations: annotations,
		StartsAt:    now,
		EndsAt:      now.Add(time.Duration(s.Config.ResolveAfter)),
	}

	if err := s.Alertmanager.Post(alert); err != nil {
		s.Logger.Error("posting to alertmanager", "mapping", mappingName, "error", err)
		http.Error(w, "posting to alertmanager failed", http.StatusBadGateway)

		return
	}

	s.Metrics.Messages.WithLabelValues(outcome, mappingName).Inc()
	w.WriteHeader(http.StatusOK)
}

// reject answers 403 and counts the message rejected. Used for a
// signature failure, an off-allow-list topic, a failed confirmation, and
// a message Type this service does not otherwise handle — every one of
// them a shape an attacker who can merely reach the public route could
// produce, and every one of them counted rather than merely logged: a
// spike here is the thing that should be noticed.
func (s *Server) reject(w http.ResponseWriter, topic, reason string) {
	s.Logger.Warn("rejected", "topic", topic, "reason", reason)
	s.Metrics.Messages.WithLabelValues("rejected", "").Inc()
	http.Error(w, "refused", http.StatusForbidden)
}
