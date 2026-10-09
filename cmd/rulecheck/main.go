// Command rulecheck parses every expression of every VMRule in the given
// YAML files or directories with the real VictoriaMetrics and VictoriaLogs
// binaries, and exits non-zero on any expression a parser refuses.
//
// It is a standalone binary, not a Go test, on purpose: `just rulecheck`
// runs it on this repository's rendered goldens in CI, and the same binary
// is what an estate runs against manifests that never enter this
// repository. See pkg/rulecheck for what is checked and why.
//
//	rulecheck [flags] <file-or-dir> [more ...]
//
// The parser versions follow the chart: by default they are read from the
// vendored charts/observability-stack archives, so they are never written
// down twice. -vm-version and -vl-version override.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/truvity/observability/pkg/rulecheck"
)

func main() {
	os.Exit(run())
}

func run() int {
	if len(os.Args) > 1 && os.Args[1] == "coverage" {
		return runCoverage(os.Args[2:])
	}

	var (
		vm     = flag.String("vm-version", "", "VictoriaMetrics release tag (default: the stack chart's)")
		vl     = flag.String("vl-version", "", "VictoriaLogs release tag (default: the stack chart's)")
		bin    = flag.String("bin-dir", os.Getenv("RULECHECK_BIN_DIR"), "directory already holding victoria-metrics-prod and victoria-logs-prod (offline)")
		cache  = flag.String("cache-dir", "", "where downloaded binaries are cached (default: under the OS temp dir)")
		stack  = flag.String("stack-charts", "charts/observability-stack/charts", "vendored dependency archives to read the default versions from")
		silent = flag.Bool("q", false, "print findings only")
		noLint = flag.Bool("no-lint", false, "parse only; skip the semantic checks")
		label  = flag.String("cluster-label", rulecheck.DefaultClusterLabel, "the label naming a series' cluster")
		reqSA  = flag.Bool("require-source-absent", false, "refuse a self-alert group without a SelfAlertSourceAbsent rule")
	)

	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: rulecheck [flags] <file-or-dir> [more ...]")
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() == 0 {
		flag.Usage()

		return 2
	}

	if *vm == "" || *vl == "" {
		v, err := rulecheck.ChartVersions(*stack)
		if err != nil {
			fmt.Fprintf(os.Stderr, "rulecheck: %v (pass -vm-version and -vl-version, or -stack-charts)\n", err)

			return 2
		}

		if *vm == "" {
			*vm = v.Metrics
		}

		if *vl == "" {
			*vl = v.Logs
		}
	}

	sources, err := rulecheck.Load("", flag.Args()...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rulecheck: %v\n", err)

		return 2
	}

	rules, err := rulecheck.Rules(sources...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rulecheck: %v\n", err)

		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	findings, err := rulecheck.Check(ctx, rules, rulecheck.Options{VMVersion: *vm, VLVersion: *vl, BinDir: *bin, CacheDir: *cache})
	if err != nil {
		fmt.Fprintf(os.Stderr, "rulecheck: %v\n", err)

		return 2
	}

	for i := range findings {
		fmt.Fprintln(os.Stderr, findings[i])
	}

	logsN := 0

	for _, r := range rules {
		if r.LogsQL() {
			logsN++
		}
	}

	if !*silent {
		fmt.Printf("rulecheck: %d expressions (%d MetricsQL on victoria-metrics %s, %d LogsQL on victoria-logs %s) in %d files, %d refused\n",
			len(rules), len(rules)-logsN, *vm, logsN, *vl, len(sources), len(findings))
	}

	var violations []rulecheck.Violation

	if !*noLint {
		violations, err = rulecheck.Lint(rules, rulecheck.LintOptions{ClusterLabel: *label, RequireSourceAbsent: *reqSA})
		if err != nil {
			fmt.Fprintf(os.Stderr, "rulecheck: %v\n", err)

			return 2
		}

		for i := range violations {
			fmt.Fprintln(os.Stderr, violations[i])
		}

		if !*silent {
			fmt.Printf("rulecheck: semantic lint: %d violations\n", len(violations))
		}
	}

	if len(findings) > 0 || len(violations) > 0 {
		return 1
	}

	return 0
}
