package tests

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// The observability-stack key freeze (restructure R0.5, step 0).
//
// charts/observability-stack is being split into per-plane charts. Until
// that is done, a new key in its values.yaml or values.schema.json is a
// key somebody will later have to move, so none may be added except a fix.
//
// The check compares every key path in both files with a committed
// allow-list, tests/stack-keys.yaml. A path in neither `keys` nor `fixes`
// fails. A fix is an explicit entry in `fixes` with a reason: adding one
// is a visible act in review. docs/contracts.md, "Adding a fix-key", says
// how.
const stackKeysFile = "stack-keys.yaml"

type stackKeys struct {
	Keys  []string   `yaml:"keys"`
	Fixes []stackFix `yaml:"fixes"`
}

type stackFix struct {
	Key    string `yaml:"key"`
	Reason string `yaml:"reason"`
}

// valuesKeyPaths lists the dotted path of every mapping key in
// values.yaml. A list is a leaf: its items are data, not keys.
func valuesKeyPaths(t *testing.T) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "charts", "observability-stack", "values.yaml"))
	require.NoError(t, err)
	var doc any
	require.NoError(t, yaml.Unmarshal(b, &doc))
	out := map[string]bool{}
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		m, ok := v.(map[string]any)
		if !ok {
			return
		}
		for k, c := range m {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			out["values:"+p] = true
			walk(p, c)
		}
	}
	walk("", doc)
	return out
}

// schemaKeyPaths lists the dotted path of every declared property in
// values.schema.json, descending through properties, items and
// additionalProperties (an item is spelled `[]`, a map value `*`).
func schemaKeyPaths(t *testing.T) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "charts", "observability-stack", "values.schema.json"))
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(b, &doc))
	out := map[string]bool{}
	var walk func(prefix string, n map[string]any)
	walk = func(prefix string, n map[string]any) {
		join := func(seg string) string {
			if prefix == "" {
				return seg
			}
			return prefix + "." + seg
		}
		if props, ok := n["properties"].(map[string]any); ok {
			for k, c := range props {
				out["schema:"+join(k)] = true
				if cm, ok := c.(map[string]any); ok {
					walk(join(k), cm)
				}
			}
		}
		if it, ok := n["items"].(map[string]any); ok {
			walk(join("[]"), it)
		}
		if ap, ok := n["additionalProperties"].(map[string]any); ok {
			walk(join("*"), ap)
		}
	}
	walk("", doc)
	return out
}

func currentStackKeys(t *testing.T) []string {
	t.Helper()
	all := valuesKeyPaths(t)
	for k := range schemaKeyPaths(t) {
		all[k] = true
	}
	out := make([]string, 0, len(all))
	for k := range all {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestStackKeysAreFrozen(t *testing.T) {
	current := currentStackKeys(t)
	require.Greater(t, len(current), 100, "found almost no keys; this test would pass vacuously")

	// STACK_KEYS_PRINT=1 prints the current keys in snapshot format. It is
	// for seeding or pruning the `keys` list, never for adding a key: a new
	// key goes in `fixes`, with a reason.
	if os.Getenv("STACK_KEYS_PRINT") != "" {
		for _, k := range current {
			fmt.Printf("  - %q\n", k)
		}
	}

	b, err := os.ReadFile(stackKeysFile)
	require.NoError(t, err)
	var snap stackKeys
	require.NoError(t, yaml.Unmarshal(b, &snap))

	allowed := map[string]bool{}
	for _, k := range snap.Keys {
		allowed[k] = true
	}
	have := map[string]bool{}
	for _, k := range current {
		have[k] = true
	}

	var added, stale, badFix []string
	fixes := map[string]bool{}
	for _, f := range snap.Fixes {
		switch {
		case strings.TrimSpace(f.Reason) == "":
			badFix = append(badFix, f.Key+": a fix needs a reason")
		case allowed[f.Key]:
			badFix = append(badFix, f.Key+": already in keys; remove it from fixes")
		case !have[f.Key]:
			badFix = append(badFix, f.Key+": no such key any more; remove the fix")
		}
		fixes[f.Key] = true
	}
	for _, k := range current {
		if !allowed[k] && !fixes[k] {
			added = append(added, k)
		}
	}
	for _, k := range snap.Keys {
		if !have[k] {
			stale = append(stale, k)
		}
	}

	require.Emptyf(t, added,
		"charts/observability-stack gained keys that are not in tests/%s:\n  %s\n\n"+
			"The stack's keys are frozen (restructure R0.5): the chart is being split by plane, so a new key is one\n"+
			"more to move. Put the setting in the chart of the plane that will own it. If this really is a FIX to the\n"+
			"stack, add under `fixes:` in tests/%s\n\n"+
			"    - key: %q\n      reason: <what is broken without it>\n\n"+
			"See docs/contracts.md, \"Adding a fix-key\".",
		stackKeysFile, strings.Join(added, "\n  "), stackKeysFile, "<the key above>")
	require.Emptyf(t, stale,
		"tests/%s lists keys the stack no longer has:\n  %s\nDelete those lines from `keys`; leaving them would let the key come back unnoticed.",
		stackKeysFile, strings.Join(stale, "\n  "))
	require.Empty(t, badFix, "tests/%s `fixes` has invalid entries", stackKeysFile)
}
