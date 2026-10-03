// Package main is the alert-ingress binary: it knows the envelope a cloud
// notification service wraps a message in, and Alertmanager's own
// POST /api/v2/alerts. See docs/alert-ingress.md for the contract this
// file and its neighbours implement.
package main

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Duration parses as a Go duration string ("15m", "1h") rather than as
// yaml.v3's own default numeric decoding of time.Duration, which would
// read `interval: 15m` as a parse error rather than fifteen minutes: the
// standard library type has no YAML mapping of its own, and guessing one
// silently is how a chart value renders and then means nothing at all.
type Duration time.Duration

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err != nil {
		return fmt.Errorf("duration: %w", err)
	}

	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("duration %q: %w", s, err)
	}

	*d = Duration(parsed)

	return nil
}

// AlertSpec is one mapping's rendered alert: `alertname` and `severity`
// are their own fields rather than living inside `labels`, so that
// nothing a mapping writes under `labels.severity` can shadow the field
// the routing tree actually keys on — the same rule
// platform-alerts.labels enforces for its own commonLabels.
type AlertSpec struct {
	Alertname   string            `yaml:"alertname"`
	Severity    string            `yaml:"severity"`
	Labels      map[string]string `yaml:"labels"`
	Annotations map[string]string `yaml:"annotations"`
	// Resolved is an optional template. When it renders to "true" the
	// alert is posted already ended (`endsAt` = now), which is how a
	// source that DOES say a state stopped being true (a CloudWatch alarm
	// going back to OK) clears the alert it raised. Empty or any other
	// value is today's behaviour: a firing alert. The labels must not
	// depend on the state, or the resolve names a different alert.
	Resolved string `yaml:"resolved"`
}

// Mapping is one entry of `mappings`, tried in the order they are
// declared; the first whose Match holds is applied and the rest are never
// consulted.
type Mapping struct {
	Name  string            `yaml:"name"`
	Match map[string]string `yaml:"match"`
	// MatchRegex is a set of RE2 patterns, each searched (unanchored) in
	// the text at a path. It is how a plain-text message is matched: use
	// `_sns.Message` or `_sns.Subject`. Every pattern here AND every
	// equality in Match must hold.
	MatchRegex map[string]string `yaml:"matchRegex"`
	Alert      AlertSpec         `yaml:"alert"`
	// ResolveAfter overrides Config.ResolveAfter for the alerts this
	// mapping posts; zero means the global value. A source that sends a
	// state change once and then stays silent for as long as the state
	// holds (a CloudWatch alarm in ALARM) needs a longer expiry than a
	// one-shot event, with the resolve as the normal way out.
	ResolveAfter Duration `yaml:"resolveAfter"`
}

// Heartbeat recognises the estate's own scheduled proof-of-life message.
// It carries no `alert:` field, on purpose: a heartbeat is COUNTED, never
// posted to Alertmanager. Its only job is to keep
// alert_ingress_messages_total{mapping="heartbeat"} moving for the
// chart's own VMRule to watch — posting it as a real alert every interval
// would page somebody for a message that means nothing went wrong.
type Heartbeat struct {
	Match    map[string]string `yaml:"match"`
	Interval Duration          `yaml:"interval"`
}

// Unmapped shapes the CloudEventUnmapped alert, so that the tree can route
// it. Labels are static strings, never templates: the unmapped path exists
// for bodies this service does not understand, and it must stay free of
// the template step. An absent block is today's behaviour: severity
// warning and no label beyond alertname.
type Unmapped struct {
	Severity string            `yaml:"severity"`
	Labels   map[string]string `yaml:"labels"`
}

// promLabelName is a Prometheus label name, the shape Alertmanager accepts.
var promLabelName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// Unmapped shapes the CloudEventUnmapped alert so the routing tree can
// route it. Labels are static strings, never templates: the unmapped path
// Config is exactly what the chart renders into the mounted ConfigMap.
// Nothing here names an estate: every particular is a value this file was
// handed.
type Config struct {
	Alertmanager struct {
		URL string `yaml:"url"`
	} `yaml:"alertmanager"`
	// Topics this receiver will confirm a subscription from and accept a
	// notification for. Required and refused empty: an endpoint that
	// confirms anything can be subscribed to anyone's topic and fed
	// alerts.
	Topics    []string  `yaml:"topics"`
	Mappings  []Mapping `yaml:"mappings"`
	Heartbeat Heartbeat `yaml:"heartbeat"`
	Unmapped  Unmapped  `yaml:"unmapped"`
	// ResolveAfter is how long after `startsAt` an alert's `endsAt` is
	// set. Cloud events do not resolve themselves — nothing tells this
	// service a GuardDuty finding stopped being true — so the alert
	// expires on a timer rather than lingering in Alertmanager forever.
	ResolveAfter Duration `yaml:"resolveAfter"`
}

// LoadConfig reads and validates the configuration the chart mounted.
// ResolveAfter defaults to one hour when the file omits it, mirroring the
// chart's own schema default; every other value is required.
func LoadConfig(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("reading config %s: %w", path, err)
	}

	cfg := Config{ResolveAfter: Duration(time.Hour)}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("parsing config %s: %w", path, err)
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}

	return cfg, nil
}

// Validate re-states, in the binary, the refusals the chart already makes
// at render time. The chart is the only thing a consumer is meant to
// edit, but this process reads a ConfigMap rather than the values file
// itself, and trusting whatever is mounted — a hand edit, a release
// rolled back to a ConfigMap an older chart version wrote — would be the
// same mistake Config.RenderClaim in pkg/tenancy refuses to make: a
// validated shape three steps upstream is not evidence about the bytes on
// disk right now.
func (c Config) Validate() error {
	if c.Alertmanager.URL == "" {
		return fmt.Errorf("alertmanager.url is empty: there is nowhere to POST an alert")
	}

	if len(c.Topics) == 0 {
		return fmt.Errorf("topics is empty: an endpoint that confirms and accepts any topic can be subscribed to anyone's topic and fed alerts")
	}

	if len(c.Heartbeat.Match) == 0 {
		return fmt.Errorf("heartbeat.match is empty: the path this service sits on could die and nothing on the estate's side would be told")
	}

	for i, m := range c.Mappings {
		for path, pattern := range m.MatchRegex {
			if _, err := regexp.Compile(pattern); err != nil {
				return fmt.Errorf("mappings[%d] (%s) matchRegex %q: %w", i, m.Name, path, err)
			}
		}

		if m.Alert.Alertname == "" {
			return fmt.Errorf("mappings[%d] (%s) has no alert.alertname", i, m.Name)
		}

		if m.ResolveAfter < 0 {
			return fmt.Errorf("mappings[%d] (%s) has a negative resolveAfter", i, m.Name)
		}

		if m.Alert.Severity == "" {
			return fmt.Errorf("mappings[%d] (%s) has no alert.severity, which routes it to the default tier by accident", i, m.Name)
		}
	}

	return c.Unmapped.validate()
}

// validate refuses an unmapped label Alertmanager would reject or that
// would shadow a field this service owns: `alertname` is fixed and
// `severity` has its own field, as in AlertSpec. An empty severity means
// the default, warning.
func (u Unmapped) validate() error {
	keys := make([]string, 0, len(u.Labels))
	for k := range u.Labels {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	for _, k := range keys {
		switch {
		case k == "":
			return fmt.Errorf("unmapped.labels has an empty label name")
		case !promLabelName.MatchString(k):
			return fmt.Errorf("unmapped.labels %q is not a valid Prometheus label name", k)
		case strings.HasPrefix(k, "__"):
			return fmt.Errorf("unmapped.labels %q is reserved: names starting with __ are internal", k)
		case k == "alertname" || k == "severity":
			return fmt.Errorf("unmapped.labels %q is reserved: alertname is fixed and severity is set by unmapped.severity", k)
		}
	}

	return nil
}
