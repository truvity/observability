// Command dashboardlint runs the six-rule contract docs/dashboards.md
// defines against one or more Grafana dashboard JSON files.
//
// It is a standalone binary, not a Go test, on purpose: `just
// dashboard-lint` runs it on charts/observability-dashboards' own set in
// this repository's CI, and the same binary is what an estate runs
// against dashboards that never enter this repository at all —
// `charts/observability-dashboards`' own extraDashboards, or a fleet
// component's. A Go test can only lint what already lives in this
// module's own tree.
package main

import (
	"fmt"
	"os"

	"github.com/truvity/observability/pkg/dashboardlint"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: dashboardlint <dashboard.json> [more.json ...]")
		os.Exit(2)
	}

	failed := false
	for _, path := range os.Args[1:] {
		findings, err := dashboardlint.LintFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			failed = true
			continue
		}
		if len(findings) == 0 {
			fmt.Printf("%s: OK\n", path)
			continue
		}
		failed = true
		for _, f := range findings {
			fmt.Fprintln(os.Stderr, f.String())
		}
	}

	if failed {
		os.Exit(1)
	}
}
