package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

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
