package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/truvity/observability/pkg/rulecheck"
)

// runCoverage implements `rulecheck coverage`: which groups and rules each
// cluster does not alert on.
//
//	rulecheck coverage [-format markdown|json] [-o file] [-catalog path]... <cluster>=<path>[,<path>...] | <file> | <dir> ...
//
// A bare file is a cluster named after it; a bare directory is one cluster
// per YAML file in it (this repository's goldens). `name=path` names the
// cluster and merges every path given under the same name.
func runCoverage(args []string) int {
	fs := flag.NewFlagSet("coverage", flag.ExitOnError)
	format := fs.String("format", "markdown", "markdown or json")
	out := fs.String("o", "", "write here instead of stdout")

	var catalog multi

	fs.Var(&catalog, "catalog", "extra file or directory whose rules are the full set (repeatable), e.g. a render with every group on")

	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: rulecheck coverage [flags] <cluster>=<path>[,<path>...] | <file> | <dir> ...")
		fs.PrintDefaults()
	}

	_ = fs.Parse(args)

	if fs.NArg() == 0 {
		fs.Usage()

		return 2
	}

	byName := map[string][]rulecheck.Rule{}

	for _, a := range fs.Args() {
		name, paths := "", []string{a}
		if n, p, ok := strings.Cut(a, "="); ok {
			name, paths = n, strings.Split(p, ",")
		}

		if name != "" {
			rs, err := load(paths...)
			if err != nil {
				return fail(err)
			}

			byName[name] = append(byName[name], rs...)

			continue
		}

		info, err := os.Stat(a)
		if err != nil {
			return fail(err)
		}

		files := []string{a}

		if info.IsDir() {
			srcs, err := rulecheck.Load("", a)
			if err != nil {
				return fail(err)
			}

			files = files[:0]

			for _, s := range srcs {
				files = append(files, s.Name)
			}
		}

		for _, f := range files {
			rs, err := load(f)
			if err != nil {
				return fail(err)
			}

			if len(rs) > 0 { // a file with no VMRule is not a cluster
				n := strings.TrimSuffix(f, filepath.Ext(f))
				byName[n] = append(byName[n], rs...)
			}
		}
	}

	var clusters []rulecheck.ClusterInput

	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}

	sort.Strings(names)

	for _, n := range names {
		clusters = append(clusters, rulecheck.ClusterInput{Name: n, Rules: byName[n]})
	}

	cat, err := load(catalog...)
	if err != nil {
		return fail(err)
	}

	rep := rulecheck.Coverage(clusters, cat)

	w := os.Stdout

	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			return fail(err)
		}

		defer func() { _ = f.Close() }()

		w = f
	}

	switch *format {
	case "json":
		err = rep.WriteJSON(w)
	case "markdown":
		err = rep.WriteMarkdown(w)
	default:
		err = fmt.Errorf("unknown -format %q", *format)
	}

	if err != nil {
		return fail(err)
	}

	return 0
}

func load(paths ...string) ([]rulecheck.Rule, error) {
	if len(paths) == 0 {
		return nil, nil
	}

	srcs, err := rulecheck.Load("", paths...)
	if err != nil {
		return nil, err
	}

	return rulecheck.Rules(srcs...)
}

func fail(err error) int {
	fmt.Fprintf(os.Stderr, "rulecheck: %v\n", err)

	return 2
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(s string) error { *m = append(*m, s); return nil }
