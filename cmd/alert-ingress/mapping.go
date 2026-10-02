package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"text/template"
)

// unmappedBodyLimit caps how much of a message's raw body a
// CloudEventUnmapped alert carries. Uncapped, a hostile or merely
// enormous payload becomes an Alertmanager annotation of the same size,
// and Alertmanager's own API has no opinion about that until something
// downstream does. Large enough to be useful in an incident, small
// enough that it never is the incident.
const unmappedBodyLimit = 4000

// templateFuncs are the helpers every template field may call, beyond
// text/template's own builtins. They exist because the builtin `ge` is
// strict about types: `ge .detail.severity 7` is an error (a JSON number
// is a float64, the literal 7 an int), `ge .detail.severity 7.0` works
// only until a publisher sends the number as a string, and a missing
// field is an error rather than "no". `atLeast` is the numeric threshold
// the severity field needs without any of those traps.
var templateFuncs = template.FuncMap{
	"num":     toNumber,
	"atLeast": atLeast,
	"reFind":  reFind,
}

// reFind returns the first capture group of the first match of the RE2
// pattern in text (the whole match if the pattern has no group), or "" when
// nothing matches. It is how a label is pulled out of a plain-text message:
// `{{ reFind "Budget Name: (\\S+)" ._sns.Message }}`. A miss is an empty
// string rather than an error, so a publisher that rewords its text degrades
// a label instead of turning the alert into CloudEventUnmapped.
func reFind(pattern, text string) (string, error) {
	re, err := compileCached(pattern)
	if err != nil {
		return "", fmt.Errorf("reFind pattern: %w", err)
	}

	m := re.FindStringSubmatch(text)
	switch {
	case m == nil:
		return "", nil
	case len(m) > 1:
		return m[1], nil
	default:
		return m[0], nil
	}
}

// toNumber converts a JSON number, an integer, or a numeric string to a
// float64. Anything else is an error, which sends the message down the
// CloudEventUnmapped path rather than silently picking a severity.
func toNumber(v any) (float64, error) {
	switch n := v.(type) {
	case float64:
		return n, nil
	case float32:
		return float64(n), nil
	case int:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case json.Number:
		return n.Float64()
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		if err != nil {
			return 0, fmt.Errorf("%q is not a number", n)
		}

		return f, nil
	default:
		return 0, fmt.Errorf("%v (%T) is not a number", v, v)
	}
}

// atLeast reports whether value is a number greater than or equal to
// threshold: `{{ if atLeast .detail.severity 7 }}critical{{ else }}warning{{ end }}`.
// A field that is absent from the message is "no" (false), so a finding
// that carries no score takes the else branch instead of failing the
// whole template; a field that is present but not numeric is an error.
func atLeast(value, threshold any) (bool, error) {
	if value == nil {
		return false, nil
	}

	v, err := toNumber(value)
	if err != nil {
		return false, fmt.Errorf("atLeast value: %w", err)
	}

	t, err := toNumber(threshold)
	if err != nil {
		return false, fmt.Errorf("atLeast threshold: %w", err)
	}

	return v >= t, nil
}

// renderString runs one template field of an `alert:` block against
// body. Every field is a template, even one with no `{{` in it: treating
// them uniformly is simpler than asking a chart consumer to remember
// which fields this service treats specially, and a plain string is its
// own template that renders to itself.
func renderString(tmpl string, body map[string]any) (string, error) {
	t, err := template.New("alert").Funcs(templateFuncs).Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("parsing template %q: %w", tmpl, err)
	}

	var buf bytes.Buffer
	if err := t.Execute(&buf, body); err != nil {
		return "", fmt.Errorf("rendering template %q: %w", tmpl, err)
	}

	return buf.String(), nil
}

// renderAlert turns one AlertSpec into the labels and annotations an
// Alertmanager alert carries. `alertname` and `severity` are written
// LAST, after the configured labels, so that nothing a mapping's own
// `labels` map happens to name `severity` can shadow the field the
// routing tree actually keys on — the same ordering
// platform-alerts.labels uses for its commonLabels.
func renderAlert(spec AlertSpec, body map[string]any) (labels, annotations map[string]string, err error) {
	labels = make(map[string]string, len(spec.Labels)+2)

	for k, v := range spec.Labels {
		rendered, err := renderString(v, body)
		if err != nil {
			return nil, nil, fmt.Errorf("label %q: %w", k, err)
		}

		labels[k] = rendered
	}

	annotations = make(map[string]string, len(spec.Annotations))

	for k, v := range spec.Annotations {
		rendered, err := renderString(v, body)
		if err != nil {
			return nil, nil, fmt.Errorf("annotation %q: %w", k, err)
		}

		annotations[k] = rendered
	}

	alertname, err := renderString(spec.Alertname, body)
	if err != nil {
		return nil, nil, fmt.Errorf("alertname: %w", err)
	}

	severity, err := renderString(spec.Severity, body)
	if err != nil {
		return nil, nil, fmt.Errorf("severity: %w", err)
	}

	labels["alertname"] = alertname
	labels["severity"] = severity

	return labels, annotations, nil
}

// unmappedAlert builds CloudEventUnmapped directly, with NO template
// step. The unmapped path exists precisely because this message's shape
// is unknown, so its body is untrusted text — running it through
// text/template, even only as the data a template executes against,
// would let a malformed body (one that happens to contain `{{`) break
// the one path that is supposed to be unbreakable: the fallback for
// everything else already failed.
func unmappedAlert(message string) (labels, annotations map[string]string) {
	labels = map[string]string{
		"alertname": "CloudEventUnmapped",
		"severity":  "warning",
	}
	annotations = map[string]string{
		"summary": "a cloud notification matched no mapping rule",
		"body":    truncate(message, unmappedBodyLimit),
	}

	return labels, annotations
}

// truncate returns s unchanged if it is at most n bytes, or its first n
// bytes with a marker appended.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}

	return s[:n] + "... (truncated)"
}
