// Package main is the alert-ingress binary: it knows the envelope a cloud
// notification service wraps a message in, and Alertmanager's own
// POST /api/v2/alerts. See docs/alert-ingress.md for the contract this
// file and its neighbours implement.
package main

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
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
}

// Mapping is one entry of `mappings`, tried in the order they are
// declared; the first whose Match holds is applied and the rest are never
// consulted.
type Mapping struct {
	Name  string            `yaml:"name"`
	Match map[string]string `yaml:"match"`
	Alert AlertSpec         `yaml:"alert"`
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
		if m.Alert.Alertname == "" {
			return fmt.Errorf("mappings[%d] (%s) has no alert.alertname", i, m.Name)
		}

		if m.Alert.Severity == "" {
			return fmt.Errorf("mappings[%d] (%s) has no alert.severity, which routes it to the default tier by accident", i, m.Name)
		}
	}

	return nil
}
