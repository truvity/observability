// A rule that matches too much is worse than one that matches too little
// — it steals a later rule's traffic the way an overlapping vmauth route
// does in charts/observability-stack, except here the "later rule" is
// the unmapped path, and a mapping that accidentally matches everything
// hides messages this repository is built to never drop QUIETLY. These
// tests are the JSON-path walk on its own, isolated from signatures and
// templates so a failure here can only mean one thing.
package main

import "testing"

func TestLookup(t *testing.T) {
	body := map[string]any{
		"detail-type": "GuardDuty Finding",
		"detail": map[string]any{
			"severity": 8.0,
			"userIdentity": map[string]any{
				"type": "Root",
			},
		},
	}

	cases := []struct {
		name string
		path string
		want any
		ok   bool
	}{
		{"a top-level key", "detail-type", "GuardDuty Finding", true},
		{"a nested key, the root-login shape", "detail.userIdentity.type", "Root", true},
		{"a key that is not there", "detail.userIdentity.arn", nil, false},
		{"a path that walks THROUGH a scalar", "detail-type.anything", nil, false},
		{"an empty path segment resolves to nothing rather than panicking", "detail..type", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := lookup(body, c.path)
			if ok != c.ok {
				t.Fatalf("lookup(%q) ok = %v, want %v", c.path, ok, c.ok)
			}

			if ok && got != c.want {
				t.Fatalf("lookup(%q) = %v, want %v", c.path, got, c.want)
			}
		})
	}
}

// TestMatchesWildcardIsPresenceOnly is the budget rule's own shape:
// `{"Message.budgetName": "*"}` asks whether the field exists at all, not
// what it says, because a threshold notification is worth an alert
// whichever budget crossed it.
func TestMatchesWildcardIsPresenceOnly(t *testing.T) {
	rule := map[string]string{"Message.budgetName": "*"}

	present := map[string]any{"Message": map[string]any{"budgetName": "anything at all"}}
	if !matches(rule, present) {
		t.Fatal("a wildcard rule must match when the field is present, whatever its value")
	}

	absent := map[string]any{"Message": map[string]any{}}
	if matches(rule, absent) {
		t.Fatal("a wildcard rule must NOT match when the field is absent")
	}
}

// TestMatchesEmptyRuleMatchesEverything is why an empty `mappings` list is
// legal rather than refused at render time: the shape itself is not the
// mistake, a consumer who forgot to write a mapping is. But a single rule
// with an empty `match` would swallow every message before the unmapped
// path ever saw one, so this is stated as a test rather than left to be
// discovered the first time somebody writes `match: {}` by accident.
func TestMatchesEmptyRuleMatchesEverything(t *testing.T) {
	if !matches(map[string]string{}, map[string]any{"anything": "at all"}) {
		t.Fatal("an empty match rule must match every body")
	}
}

func TestMatchesRequiresEveryEquality(t *testing.T) {
	rule := map[string]string{
		"detail-type":              "AWS Console Sign In via CloudTrail",
		"detail.userIdentity.type": "Root",
	}

	root := map[string]any{
		"detail-type": "AWS Console Sign In via CloudTrail",
		"detail":      map[string]any{"userIdentity": map[string]any{"type": "Root"}},
	}
	if !matches(rule, root) {
		t.Fatal("a body satisfying every equality must match")
	}

	iamUser := map[string]any{
		"detail-type": "AWS Console Sign In via CloudTrail",
		"detail":      map[string]any{"userIdentity": map[string]any{"type": "IAMUser"}},
	}
	if matches(rule, iamUser) {
		t.Fatal("a body satisfying only ONE of two equalities must not match — an ordinary console" +
			" sign-in would otherwise page as a root sign-in")
	}
}
