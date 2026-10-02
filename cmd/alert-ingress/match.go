package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// envelopeKey is the one top-level key under which a message's SNS
// envelope is exposed to mappings: `_sns.TopicArn`, `_sns.Subject`,
// `_sns.Message` (the raw text of the Message field, whatever it is),
// `_sns.MessageId` and `_sns.Type`. It is set AFTER the message is
// parsed and overwrites whatever the message itself put there, so a
// publisher cannot forge it: the envelope fields are covered by the
// signature, the body keys are only the publisher's say-so. The leading
// underscore keeps it clear of every key a provider's own event schema
// uses, so existing mappings are unaffected.
const envelopeKey = "_sns"

// buildInput is what mappings, the heartbeat and templates are matched
// and rendered against: the parsed Message body (empty for plain text)
// plus the envelope under envelopeKey.
func buildInput(env Envelope) map[string]any {
	body := parseBody(env.Message)
	if body == nil {
		body = map[string]any{}
	}

	body[envelopeKey] = map[string]any{
		"TopicArn":  env.TopicArn,
		"Subject":   env.Subject,
		"Message":   env.Message,
		"MessageId": env.MessageID,
		"Type":      env.Type,
	}

	return body
}

// regexCache holds compiled matchRegex patterns. The patterns come from
// the operator's own configuration, so the set is fixed and small.
var regexCache sync.Map

func compileCached(pattern string) (*regexp.Regexp, error) {
	if re, ok := regexCache.Load(pattern); ok {
		return re.(*regexp.Regexp), nil
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}

	regexCache.Store(pattern, re)

	return re, nil
}

// matchesRegex reports whether every pattern in rule finds a match
// (unanchored, RE2) in the value at its path. A path that is absent never
// matches. Go's regexp is linear-time, so a pattern cannot be made to
// backtrack catastrophically against a hostile message.
func matchesRegex(rule map[string]string, body map[string]any) bool {
	for path, pattern := range rule {
		got, ok := lookup(body, path)
		if !ok {
			return false
		}

		re, err := compileCached(pattern)
		if err != nil || !re.MatchString(fmt.Sprintf("%v", got)) {
			return false
		}
	}

	return true
}

// matchesMapping is the whole of a mapping's condition: every `match`
// equality and every `matchRegex` pattern must hold.
func matchesMapping(m Mapping, body map[string]any) bool {
	return matches(m.Match, body) && matchesRegex(m.MatchRegex, body)
}

// parseBody decodes the notification's real content. The provider's
// envelope carries the event as a JSON STRING in `Message`; this is that
// string decoded one level, and every match path and every alert
// template in this service reads against it and nothing else. The
// envelope's own fields — MessageId, TopicArn, the signature — were the
// signature's business and are never reachable from a mapping.
//
// A message that is not a JSON object — a plain string, or malformed
// JSON — decodes to an empty object rather than an error. It then
// matches no rule and falls through to the unmapped path, which is
// exactly what should happen to a shape this service was never told
// about: never a crash, never a silent drop.
func parseBody(message string) map[string]any {
	var body map[string]any
	_ = json.Unmarshal([]byte(message), &body)

	return body
}

// lookup resolves a dot-separated path against nested JSON objects — the
// walk `detail.userIdentity.type` in docs/alert-ingress.md describes.
func lookup(body map[string]any, path string) (any, bool) {
	var cur any = body

	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}

		v, ok := m[part]
		if !ok {
			return nil, false
		}

		cur = v
	}

	return cur, true
}

// matches reports whether every equality in rule holds against body. A
// value of "*" tests presence only, which is how the budget mapping in
// docs/alert-ingress.md asks "does this shape have a budgetName at all"
// without caring what it is. An empty rule matches everything, which is
// why an empty `mappings` list is legal (if pointless): the chart's own
// refusal is about a consumer who forgot to write a mapping, not about
// this shape.
func matches(rule map[string]string, body map[string]any) bool {
	for path, want := range rule {
		got, ok := lookup(body, path)
		if !ok {
			return false
		}

		if want == "*" {
			continue
		}

		if fmt.Sprintf("%v", got) != want {
			return false
		}
	}

	return true
}
