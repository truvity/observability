package tests

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The third boundary: what exists, what is checked, and what is published.
//
// A chart is named in three places — the directory it lives in, the
// Justfile list that lints and goldens it, and the release workflow's
// list that pushes it to the registry. Nothing joins them, and the way
// they come apart is not an error: `observability-emitters` was added to
// the first two and not the third, so the next tag would have published
// three charts, reported success, and left the fourth unpublished until
// somebody went looking for a version that was never there.
//
// That is the expensive direction, because a tag is spent. The version
// cannot be re-cut; the fix is a new one, and anybody who pinned the
// missing chart at the old number finds nothing.
func TestEveryChartIsLintedAndPublished(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("..", "charts"))
	require.NoError(t, err)

	var onDisk []string
	for _, e := range entries {
		if e.IsDir() {
			onDisk = append(onDisk, e.Name())
		}
	}
	require.NotEmpty(t, onDisk, "no charts found; this test would pass vacuously")
	sort.Strings(onDisk)

	assert.Equal(t, onDisk, justfileCharts(t),
		"the Justfile's chart list and charts/ disagree: a chart missing here is never linted, and its goldens never render")
	assert.Equal(t, onDisk, releaseCharts(t),
		"the release workflow's chart list and charts/ disagree: a chart missing here is never published, and the tag that should have carried it is spent")
}

// justfileCharts reads the `charts := "..."` assignment rather than
// running just, so the test says which list is wrong rather than that
// something failed.
func justfileCharts(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "Justfile"))
	require.NoError(t, err)

	m := regexp.MustCompile(`(?m)^charts\s*:=\s*"([^"]*)"`).FindSubmatch(b)
	require.NotNil(t, m, "no `charts := \"...\"` assignment in the Justfile")

	out := strings.Fields(string(m[1]))
	sort.Strings(out)
	return out
}

// releaseCharts reads the JSON array the release workflow passes to the
// shared publisher.
func releaseCharts(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "release.yaml"))
	require.NoError(t, err)

	m := regexp.MustCompile(`(?m)^\s*charts:\s*'(\[[^']*\])'`).FindSubmatch(b)
	require.NotNil(t, m, "no `charts: '[...]'` input in the release workflow")

	var out []string
	require.NoError(t, json.Unmarshal(m[1], &out))
	sort.Strings(out)
	return out
}

// The fourth boundary: what a release announces, and what it tells the
// operator to do about it.
//
// CHANGELOG.md says what changed and why. docs/adoption.md, under
// "## Upgrades that change what runs", says what somebody running this
// has to DO before the bump, in the order it has to happen — and that
// section's own opening line calls none of it optional reading. Nothing
// joined the two, and the way they came apart is the invisible kind:
// the section read "Nothing yet — the first release has not been cut"
// through three releases, two of them breaking, and was written up only
// afterwards. Every check was green the whole time, because no check
// knew the section was there.
//
// The cost lands on somebody else. A consumer who reads the changelog
// learns that a value became required; a consumer who reads the upgrade
// list learns what to set it to and what to do first. With nothing in
// the list, they learn it from a render that refuses, during the
// upgrade, having already decided the upgrade was safe.
//
// The rule is deliberately not "every version needs an entry".
// CHANGELOG.md's own header says a version missing from it changed
// nothing for a consumer, and a release can honestly ask nothing of an
// operator. A list padded with entries saying there is nothing to do
// stops being read, which is the same failure by a longer route. So the
// gate is keyed on the announcement the changelog already makes: a bold
// **Breaking marker, which is how every breaking entry in that file
// opens and is the word whoever writes one is typing anyway. An entry
// for a release that is not marked breaking is allowed — operator work
// is not always a break — but a marked one without an entry is not.
func TestEveryBreakingReleaseSaysHowToUpgradeIntoIt(t *testing.T) {
	releases := changelogReleases(t)
	require.Greater(t, len(releases), 1,
		"fewer than two releases in CHANGELOG.md; this test would pass vacuously")

	// Newest first, as the file's own header states. The order is
	// load-bearing here and not decoration: it is what says which release
	// an upgrade entry spans.
	for i := 0; i+1 < len(releases); i++ {
		require.Truef(t, versionLess(releases[i+1].version, releases[i].version),
			"CHANGELOG.md lists %s above %s. The file is newest first, and this check reads that order to\n"+
				"work out which release each upgrade entry is an upgrade from.",
			releases[i].version, releases[i+1].version)
	}

	// The oldest version listed is the first release. There is no version
	// to upgrade from, so it has no entry and must not have one. That is
	// written down rather than left to the data, because the first
	// release's own section does carry a **Breaking marker — for a
	// default it refused to guess — and a reader of this test should not
	// have to infer the exemption from the fact that it passes.
	first := releases[len(releases)-1].version
	previous := make(map[string]string, len(releases))
	for i := 0; i+1 < len(releases); i++ {
		previous[releases[i].version] = releases[i+1].version
	}

	entries := adoptionUpgrades(t)
	require.NotEmpty(t, entries,
		"no `### <from> → <to>` entries under \"## Upgrades that change what runs\" in docs/adoption.md; this test would pass vacuously")

	byTarget := make(map[string]upgradeEntry, len(entries))
	for _, e := range entries {
		if prior, dup := byTarget[e.to]; dup {
			assert.Failf(t, "two upgrade entries for one release",
				"docs/adoption.md has both \"### %s → %s\" and \"### %s → %s\". One release, one entry:\n"+
					"an operator reads the first one they find, and whichever that is, half the work is in the other.",
				prior.from, prior.to, e.from, e.to)
			continue
		}
		byTarget[e.to] = e
	}

	// Forwards: a release that announces a break must say how to get into
	// it. This is the defect the check exists for.
	for _, r := range releases {
		if !r.breaking || r.version == first {
			continue
		}
		if _, ok := byTarget[r.version]; ok {
			continue
		}
		assert.Failf(t, "a breaking release with no upgrade entry",
			"CHANGELOG.md announces a breaking change in %[1]s, and docs/adoption.md has no upgrade entry for it.\n"+
				"\n"+
				"Add one under \"## Upgrades that change what runs\", newest first, headed exactly:\n"+
				"\n"+
				"    ### %[2]s → %[1]s\n"+
				"\n"+
				"Under it goes what an operator must DO, in the order it has to happen: the values to set before the\n"+
				"bump, the renders that will move and should be let through, and anything that stops working. What\n"+
				"changed and why stays in CHANGELOG.md and is not repeated there — the entry is the work.\n"+
				"\n"+
				"If %[1]s in fact asks nothing of an operator, then it is not breaking: take the bold \"Breaking\"\n"+
				"marker off its changelog entry. Do not write an entry that says there is nothing to do — a list\n"+
				"with those in it is a list nobody reads the rest of.",
			r.version, previous[r.version])
	}

	// Backwards: an entry for a release that does not exist. A mistyped
	// version number produces exactly this, and nothing else reports it —
	// the entry is present, so the release looks documented, while the
	// operator upgrading to the real version finds no heading for it.
	known := make(map[string]bool, len(releases))
	for _, r := range releases {
		known[r.version] = true
	}
	var versions []string
	for _, r := range releases {
		versions = append(versions, r.version)
	}

	for _, e := range entries {
		if !known[e.to] {
			assert.Failf(t, "an upgrade entry for a release that was never cut",
				"docs/adoption.md has \"### %[1]s → %[2]s\", but CHANGELOG.md has no \"## %[2]s\" section. Either the\n"+
					"version is mistyped, or the entry was written for a release that has not been cut — the releases\n"+
					"the changelog knows are %[3]s.\n"+
					"\n"+
					"Nothing else catches this: the entry exists, so the work looks written up, and the operator\n"+
					"upgrading searches for a heading that is not there.",
				e.from, e.to, strings.Join(versions, ", "))
			continue
		}
		if e.to == first {
			assert.Failf(t, "an upgrade entry into the first release",
				"docs/adoption.md has \"### %s → %s\", but %s is the first release in CHANGELOG.md. There is no\n"+
					"version to upgrade from, and an installation, not an upgrade, is what docs/adoption.md covers above.",
				e.from, e.to, e.to)
			continue
		}
		if want := previous[e.to]; e.from != want {
			assert.Failf(t, "an upgrade entry that spans the wrong releases",
				"docs/adoption.md has \"### %[1]s → %[2]s\", but the release before %[2]s in CHANGELOG.md is %[3]s.\n"+
					"The heading must read:\n"+
					"\n"+
					"    ### %[3]s → %[2]s\n"+
					"\n"+
					"An entry spans every version that changed something for a consumer, and CHANGELOG.md is that\n"+
					"list — a version missing from it changed nothing, so it is never one end of an arrow. A heading\n"+
					"naming any other version reads as though whoever is on %[3]s had nothing to do, or, with the two\n"+
					"ends the other way round, as a downgrade.",
				e.from, e.to, want)
		}
	}
}

// release is a version heading in CHANGELOG.md and whether that version's
// section announces a break.
type release struct {
	version  string
	breaking bool
}

// upgradeEntry is one `### <from> → <to>` heading in docs/adoption.md.
type upgradeEntry struct {
	from, to string
}

var (
	changelogHeading = regexp.MustCompile(`^## (\d+\.\d+\.\d+)\s*$`)
	breakingMarker   = regexp.MustCompile(`\*\*Breaking\b`)
	adoptionHeading  = regexp.MustCompile(`^### (\d+\.\d+\.\d+) → (\d+\.\d+\.\d+)\s*$`)
)

// changelogReleases reads the version headings in file order, newest
// first, and whether each section carries the bold Breaking marker.
func changelogReleases(t *testing.T) []release {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "CHANGELOG.md"))
	require.NoError(t, err)

	var out []release
	for _, line := range strings.Split(string(b), "\n") {
		if m := changelogHeading.FindStringSubmatch(line); m != nil {
			out = append(out, release{version: m[1]})
			continue
		}
		if len(out) > 0 && breakingMarker.MatchString(line) {
			out[len(out)-1].breaking = true
		}
	}
	return out
}

// adoptionUpgrades reads the headings inside docs/adoption.md's upgrade
// section only. A `###` heading of that shape anywhere else in the
// document is somebody's example, not an entry, and the section has to be
// found by name rather than assumed so that renaming it away fails here
// instead of emptying the check.
func adoptionUpgrades(t *testing.T) []upgradeEntry {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "docs", "adoption.md"))
	require.NoError(t, err)

	const section = "## Upgrades that change what runs"
	var found, inSection bool
	var out []upgradeEntry
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "## ") {
			inSection = strings.TrimSpace(line) == section
			found = found || inSection
			continue
		}
		if !inSection {
			continue
		}
		if m := adoptionHeading.FindStringSubmatch(line); m != nil {
			out = append(out, upgradeEntry{from: m[1], to: m[2]})
		}
	}
	require.Truef(t, found,
		"docs/adoption.md has no %q section.\n"+
			"\n"+
			"That is where a release says what an operator must do before a bump, and this check reads it by that\n"+
			"name: renaming the section does not remove the obligation, it removes the check.",
		section)
	return out
}

// versionLess orders two `x.y.z` strings numerically. The headings these
// come from are matched on that shape, so there is nothing else to parse.
func versionLess(a, b string) bool {
	af, bf := strings.Split(a, "."), strings.Split(b, ".")
	for i := range af {
		x, _ := strconv.Atoi(af[i])
		y, _ := strconv.Atoi(bf[i])
		if x != y {
			return x < y
		}
	}
	return false
}
