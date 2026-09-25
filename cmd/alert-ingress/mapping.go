package main

import (
	"bytes"
	"fmt"
	"text/template"
)

// unmappedBodyLimit caps how much of a message's raw body a
// CloudEventUnmapped alert carries. Uncapped, a hostile or merely
// enormous payload becomes an Alertmanager annotation of the same size,
// and Alertmanager's own API has no opinion about that until something
// downstream does. Large enough to be useful in an incident, small
// enough that it never is the incident.
const unmappedBodyLimit = 4000

// renderString runs one template field of an `alert:` block against
// body. Every field is a template, even one with no `{{` in it: treating
// them uniformly is simpler than asking a chart consumer to remember
// which fields this service treats specially, and a plain string is its
// own template that renders to itself.
func renderString(tmpl string, body map[string]any) (string, error) {
	t, err := template.New("alert").Parse(tmpl)
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
