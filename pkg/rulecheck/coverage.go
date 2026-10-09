package rulecheck

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// ClusterInput is the rules one cluster renders: every source that belongs
// to it, merged.
type ClusterInput struct {
	Name  string
	Rules []Rule
}

// ClusterCoverage is what one cluster does and does not alert on, measured
// against the catalog (every group and rule any cluster in the report, or
// the extra catalog, renders).
type ClusterCoverage struct {
	Cluster string `json:"cluster"`
	Groups  int    `json:"groups"`
	Rules   int    `json:"rules"`
	// DisabledGroups are catalog groups this cluster renders none of.
	DisabledGroups []string `json:"disabledGroups"`
	// DisabledRules are catalog rules (as "group/name") missing from a
	// group the cluster does render.
	DisabledRules []string `json:"disabledRules"`
	// UncoveredSelfAlertSources are metrics a self-alert reads that no
	// SelfAlertSourceAbsent rule watches ("metric (read by alert, ...)").
	UncoveredSelfAlertSources []string `json:"uncoveredSelfAlertSources"`
}

// Report is the coverage of every cluster.
type Report struct {
	CatalogGroups int               `json:"catalogGroups"`
	CatalogRules  int               `json:"catalogRules"`
	Clusters      []ClusterCoverage `json:"clusters"`
}

// Coverage compares each cluster's rules with the union of all of them and
// the optional extra catalog (rules rendered with everything switched on).
// A group or rule is "disabled" for a cluster when the catalog has it and
// the cluster does not: the rendered manifests carry only what is enabled,
// so a gap in coverage is visible only against something that has it.
func Coverage(clusters []ClusterInput, catalog []Rule) Report {
	cat := map[string]map[string]bool{} // group -> rule names

	addCat := func(rs []Rule) {
		for _, r := range rs {
			if cat[r.Group] == nil {
				cat[r.Group] = map[string]bool{}
			}

			cat[r.Group][r.Name] = true
		}
	}

	addCat(catalog)

	for _, c := range clusters {
		addCat(c.Rules)
	}

	rep := Report{CatalogGroups: len(cat)}
	for _, g := range cat {
		rep.CatalogRules += len(g)
	}

	for _, c := range clusters {
		have := map[string]map[string]bool{}

		for _, r := range c.Rules {
			if have[r.Group] == nil {
				have[r.Group] = map[string]bool{}
			}

			have[r.Group][r.Name] = true
		}

		cc := ClusterCoverage{Cluster: c.Name, Groups: len(have), DisabledGroups: []string{}, DisabledRules: []string{}, UncoveredSelfAlertSources: []string{}}
		for _, g := range have {
			cc.Rules += len(g)
		}

		for g, names := range cat {
			if have[g] == nil {
				cc.DisabledGroups = append(cc.DisabledGroups, g)

				continue
			}

			for n := range names {
				if !have[g][n] {
					cc.DisabledRules = append(cc.DisabledRules, g+"/"+n)
				}
			}
		}

		byRes := map[string][]Rule{}
		for _, r := range c.Rules {
			byRes[r.Source+"\x00"+r.Resource] = append(byRes[r.Source+"\x00"+r.Resource], r)
		}

		for _, rs := range byRes {
			for _, gap := range sourceAbsentGaps(rs) {
				cc.UncoveredSelfAlertSources = append(cc.UncoveredSelfAlertSources, gap.msg)
			}
		}

		sort.Strings(cc.DisabledGroups)
		sort.Strings(cc.DisabledRules)
		sort.Strings(cc.UncoveredSelfAlertSources)
		rep.Clusters = append(rep.Clusters, cc)
	}

	sort.Slice(rep.Clusters, func(i, j int) bool { return rep.Clusters[i].Cluster < rep.Clusters[j].Cluster })

	return rep
}

// WriteJSON writes the report as indented JSON.
func (r Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")

	return enc.Encode(r)
}

// WriteMarkdown writes the report for a job summary or a human.
func (r Report) WriteMarkdown(w io.Writer) error {
	var b strings.Builder

	fmt.Fprintf(&b, "## rulecheck coverage\n\nCatalog: %d groups, %d rules. A group or rule is *disabled* for a cluster when the catalog has it and the cluster renders none.\n\n", r.CatalogGroups, r.CatalogRules)
	b.WriteString("| cluster | groups | rules | disabled groups | disabled rules | self-alert sources without sourceAbsent |\n|---|---:|---:|---:|---:|---:|\n")

	for _, c := range r.Clusters {
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %d |\n", c.Cluster, c.Groups, c.Rules, len(c.DisabledGroups), len(c.DisabledRules), len(c.UncoveredSelfAlertSources))
	}

	for _, c := range r.Clusters {
		if len(c.DisabledGroups)+len(c.DisabledRules)+len(c.UncoveredSelfAlertSources) == 0 {
			continue
		}

		fmt.Fprintf(&b, "\n<details><summary>%s</summary>\n\n", c.Cluster)

		section := func(title string, items []string) {
			if len(items) == 0 {
				return
			}

			fmt.Fprintf(&b, "**%s**\n\n", title)

			for _, it := range items {
				fmt.Fprintf(&b, "- %s\n", it)
			}

			b.WriteString("\n")
		}

		section("Disabled groups", c.DisabledGroups)
		section("Disabled rules", c.DisabledRules)
		section("Self-alert sources without sourceAbsent coverage", c.UncoveredSelfAlertSources)
		b.WriteString("</details>\n")
	}

	_, err := io.WriteString(w, b.String())

	return err
}
