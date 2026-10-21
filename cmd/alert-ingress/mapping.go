package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"text/template"
	"unicode/utf8"
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
	"num":               toNumber,
	"atLeast":           atLeast,
	"reFind":            reFind,
	"get":               getPath,
	"guarddutyResource": guarddutyResource,
}

// getPath (the template function `get`) returns the value at a
// dot-separated path under root, or "" when any step of the path is absent
// or null: `{{ get . "detail.service.count" }}`. The template's own
// `.a.b.c` prints "<no value>" for a missing field, which is noise in a
// human-readable annotation, and is an ERROR when a step is present but not
// an object, which sends the whole message down the CloudEventUnmapped
// path. An optional field (a finding's count, an API call's caller address)
// must degrade to an empty string instead, which `with` then skips. A
// present value is returned as it is, so a number stays a number for `num`
// and `atLeast`.
func getPath(root any, path string) any {
	m, ok := root.(map[string]any)
	if !ok {
		return ""
	}

	v, ok := lookup(m, path)
	if !ok || v == nil {
		return ""
	}

	return v
}

// guarddutyResource is a one-line description of the resource a GuardDuty
// finding is about, read from the finding's `detail.resource`:
// `{{ guarddutyResource .detail.resource }}`. It names the resource type
// and the identifier a responder looks up first (an instance id, an access
// key and its user, a bucket, a cluster and workload, a database, a
// function); a type it does not know is named by its type alone, and an
// absent resource is "". It never fails: it feeds a description, and a
// description must not be what turns a finding into CloudEventUnmapped.
func guarddutyResource(resource any) string {
	r, ok := resource.(map[string]any)
	if !ok {
		return ""
	}

	str := func(path string) string {
		v, ok := lookup(r, path)
		if !ok || v == nil {
			return ""
		}

		return strings.TrimSpace(fmt.Sprint(v))
	}

	typ := str("resourceType")
	var parts []string

	add := func(format string, values ...string) {
		for _, v := range values {
			if v == "" {
				return
			}
		}

		args := make([]any, len(values))
		for i, v := range values {
			args[i] = v
		}

		parts = append(parts, fmt.Sprintf(format, args...))
	}

	switch typ {
	case "AccessKey":
		add("%s", str("accessKeyDetails.accessKeyId"))
		add("user %s", str("accessKeyDetails.userName"))
		add("(%s)", str("accessKeyDetails.userType"))
	case "Instance":
		add("%s", str("instanceDetails.instanceId"))
		add("(%s)", str("instanceDetails.instanceType"))
	case "S3Bucket":
		if buckets, ok := r["s3BucketDetails"].([]any); ok {
			var names []string

			for _, b := range buckets {
				if bm, ok := b.(map[string]any); ok {
					if n, ok := bm["name"].(string); ok && n != "" {
						names = append(names, n)
					}
				}
			}

			add("%s", strings.Join(names, ", "))
		}
	case "EKSCluster":
		add("%s", str("eksClusterDetails.name"))
		add("workload %s/%s", str("kubernetesDetails.kubernetesWorkloadDetails.namespace"),
			str("kubernetesDetails.kubernetesWorkloadDetails.name"))
		add("user %s", str("kubernetesDetails.kubernetesUserDetails.username"))
	case "ECSCluster":
		add("%s", str("ecsClusterDetails.name"))
	case "Container":
		add("%s", str("containerDetails.name"))
		add("image %s", str("containerDetails.image"))
	case "RDSDBInstance":
		add("%s", str("rdsDbInstanceDetails.dbInstanceIdentifier"))
		add("user %s", str("rdsDbUserDetails.user"))
	case "RDSLimitlessDB":
		add("%s", str("rdsLimitlessDbDetails.dbShardGroupIdentifier"))
	case "Lambda":
		add("%s", str("lambdaDetails.functionName"))
	}

	if len(parts) == 0 {
		return typ
	}

	if typ == "" {
		return strings.Join(parts, " ")
	}

	return typ + " " + strings.Join(parts, " ")
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

// renderResolved reports whether spec's `resolved` template renders to
// "true" (case-insensitive, surrounding space ignored) against body. An
// empty field is false without running a template. Any other output is
// false too: only an explicit "true" ends an alert, so a template that
// drifts from what it was written for leaves the alert firing, the safe
// side of the two mistakes.
func renderResolved(spec AlertSpec, body map[string]any) (bool, error) {
	if spec.Resolved == "" {
		return false, nil
	}

	out, err := renderString(spec.Resolved, body)
	if err != nil {
		return false, fmt.Errorf("resolved: %w", err)
	}

	return strings.EqualFold(strings.TrimSpace(out), "true"), nil
}

// unmappedInput is what the unmapped path knows about a message: only the
// signed envelope's own fields and, when a mapping's template failed, which
// mapping and why.
type unmappedInput struct {
	Topic   string
	Subject string
	Message string
	// Mapping and Cause are set when a mapping matched but its template
	// failed to render; empty when no mapping matched at all.
	Mapping string
	Cause   string
}

// unmappedAlert builds CloudEventUnmapped directly, with NO template
// step. The unmapped path exists precisely because this message's shape
// is unknown, so its body is untrusted text — running it through
// text/template, even only as the data a template executes against,
// would let a malformed body (one that happens to contain `{{`) break
// the one path that is supposed to be unbreakable: the fallback for
// everything else already failed.
//
// The annotations follow the notification convention (docs/notifications.md,
// "Annotations"): `summary` is the message's own Subject, or names the topic
// when there is none; `description` is the start of the body (indented when
// it is JSON) so the notification itself shows what arrived; `body` keeps
// the longer raw copy for the console.
//
// cfg adds static labels and may change the severity; alertname and
// severity are written last so a label can never shadow them.
func unmappedAlert(cfg Unmapped, in unmappedInput) (labels, annotations map[string]string) {
	severity := cfg.Severity
	if severity == "" {
		severity = "warning"
	}

	labels = make(map[string]string, len(cfg.Labels)+2)
	for k, v := range cfg.Labels {
		labels[k] = v
	}

	labels["alertname"] = "CloudEventUnmapped"
	labels["severity"] = severity

	summary := strings.TrimSpace(in.Subject)
	if summary == "" {
		summary = "Unmapped cloud notification from topic " + topicName(in.Topic)
	}

	description := truncate(prettyBody(in.Message), unmappedDescriptionLimit)
	if in.Mapping != "" {
		description = fmt.Sprintf("Mapping %q matched but failed to render: %s\n\n%s", in.Mapping, truncate(in.Cause, 300), description)
		summary = "Mapping " + in.Mapping + " failed to render: " + summary
	} else {
		description = "No mapping rule matched this notification from topic " + topicName(in.Topic) + ".\n\n" + description
	}

	annotations = map[string]string{
		"summary":     summary,
		"description": description,
		"body":        truncate(in.Message, unmappedBodyLimit),
	}

	if in.Topic != "" {
		annotations["topic"] = in.Topic
	}

	return labels, annotations
}

// unmappedDescriptionLimit caps the part of an unmapped body that goes into
// `description`, the annotation a notification prints. `body` keeps the
// longer copy (unmappedBodyLimit).
const unmappedDescriptionLimit = 1000

// topicName is the last segment of a topic ARN (`...:security-alerts` →
// `security-alerts`), or the whole string when it has no `:`.
func topicName(topic string) string {
	if topic == "" {
		return "(unknown)"
	}

	if i := strings.LastIndex(topic, ":"); i >= 0 && i < len(topic)-1 {
		return topic[i+1:]
	}

	return topic
}

// prettyBody indents a JSON body for a human; any other text is returned
// trimmed and otherwise as it is. Only json.Indent touches it: no template,
// no decoding into values that could be executed.
func prettyBody(message string) string {
	trimmed := strings.TrimSpace(message)

	var buf bytes.Buffer
	if json.Valid([]byte(trimmed)) && json.Indent(&buf, []byte(trimmed), "", "  ") == nil {
		return buf.String()
	}

	return trimmed
}

// truncate returns s unchanged if it is at most n bytes, or its first n
// bytes (backed off to the start of a character) with a marker appended.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}

	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}

	return s[:cut] + "... (truncated)"
}
