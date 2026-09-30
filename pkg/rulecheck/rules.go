// Package rulecheck parses every alerting and recording expression in a
// set of VMRule documents with the REAL VictoriaMetrics and VictoriaLogs
// binaries, so an expression neither of them can parse is found before it
// reaches a cluster.
//
// Why: a `type: vlogs` rule containing `| filter successes:==0` is not
// LogsQL (an exact match is `:=0`). The VM operator's admission webhook
// refuses the whole VMRule, the Application that carries it fails to sync,
// and everything queued behind it waits. Every other check passes: a
// golden compares TEXT, and text is valid YAML whatever the expression
// inside it says.
//
// What is checked: every `kind: VMRule` document handed in. A group with no
// `type`, or `prometheus`, is MetricsQL and goes to victoria-metrics'
// /api/v1/query. A `vlogs` group is LogsQL and goes to victoria-logs'
// /select/logsql/stats_query -- the endpoint vmalert itself evaluates a
// vlogs rule through, so a LogsQL expression that is valid but not a stats
// query (which no alert can use) is refused as well. Any other type is an
// error, never a skip.
//
// Why binaries and not containers: CI runs on runner pods, where a Docker
// daemon is not a given. The release binaries are the same code the images
// carry, need no daemon, and run identically on a laptop. They are
// downloaded from the projects' GitHub releases, verified against the
// checksum file published with each release, and cached. A download that
// fails is an error, not a skip: a gate that steps aside when the network
// is away is a gate a bad rule can walk through. Options.BinDir names a
// directory already holding `victoria-metrics-prod` and
// `victoria-logs-prod` for offline use.
package rulecheck

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Source is one named YAML stream (a file, or a rendered manifest) that may
// hold several documents.
type Source struct {
	Name string
	YAML []byte
}

// Rule is one expression together with where it came from, so a finding
// names the rule and not just the text.
type Rule struct {
	// Source is the file (or stream) name; Resource the VMRule's
	// metadata.name.
	Source, Resource string
	Group, Name      string
	// Type is the group's `type` as written; empty means prometheus.
	Type string
	Expr string
}

// LogsQL reports whether the rule is a vlogs (LogsQL) rule; otherwise it is
// MetricsQL.
func (r Rule) LogsQL() bool { return r.Type == typeVLogs }

func (r Rule) language() string {
	if r.LogsQL() {
		return "LogsQL"
	}

	return "MetricsQL"
}

// String names the rule the way a finding does.
func (r Rule) String() string {
	return fmt.Sprintf("%s: VMRule %q group %q rule %q (%s)", r.Source, r.Resource, r.Group, r.Name, r.language())
}

const (
	typeVLogs      = "vlogs"
	typePrometheus = "prometheus"
)

// Rules reads every VMRule document in the sources and returns each rule's
// expression. A group of an unknown type is an error.
func Rules(sources ...Source) ([]Rule, error) {
	var out []Rule

	for _, s := range sources {
		rules, err := rulesIn(s)
		if err != nil {
			return nil, err
		}

		out = append(out, rules...)
	}

	return out, nil
}

func rulesIn(s Source) ([]Rule, error) {
	var out []Rule

	dec := yaml.NewDecoder(strings.NewReader(string(s.YAML)))

	for {
		var doc struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
			Spec struct {
				Groups []struct {
					Name  string `yaml:"name"`
					Type  string `yaml:"type"`
					Rules []struct {
						Alert  string `yaml:"alert"`
						Record string `yaml:"record"`
						Expr   string `yaml:"expr"`
					} `yaml:"rules"`
				} `yaml:"groups"`
			} `yaml:"spec"`
		}

		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return nil, fmt.Errorf("%s: parse: %w", s.Name, err)
		}

		if doc.Kind != "VMRule" {
			continue
		}

		for _, g := range doc.Spec.Groups {
			typ := g.Type

			switch typ {
			case "", typePrometheus:
				typ = ""
			case typeVLogs:
			default:
				return nil, fmt.Errorf("%s: VMRule %q group %q has type %q; this check knows prometheus and vlogs -- teach it the new one rather than skip its rules",
					s.Name, doc.Metadata.Name, g.Name, g.Type)
			}

			for _, r := range g.Rules {
				name := r.Alert
				if name == "" {
					name = r.Record
				}

				out = append(out, Rule{
					Source: s.Name, Resource: doc.Metadata.Name, Group: g.Name, Name: name,
					Type: typ, Expr: r.Expr,
				})
			}
		}
	}

	return out, nil
}

// Load reads YAML files and directories (walked recursively for *.yaml and
// *.yml, in sorted order) into Sources named by their path relative to
// base, or as given when that is not possible.
func Load(base string, paths ...string) ([]Source, error) {
	var files []string

	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}

		if !info.IsDir() {
			files = append(files, p)

			continue
		}

		err = filepath.WalkDir(p, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}

			if ext := filepath.Ext(path); !d.IsDir() && (ext == ".yaml" || ext == ".yml") {
				files = append(files, path)
			}

			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	sort.Strings(files)

	out := make([]Source, 0, len(files))

	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}

		name := f
		if base != "" {
			if rel, err := filepath.Rel(base, f); err == nil {
				name = rel
			}
		}

		out = append(out, Source{Name: name, YAML: raw})
	}

	return out, nil
}
