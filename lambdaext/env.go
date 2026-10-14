package lambdaext

import (
	"slices"
	"strings"
)

const (
	// envPrefix is the generic prefix of every setting.
	envPrefix = "OTLP_LAMBDA_"
	// sluisEnvPrefix and legacyEnvPrefix are the two earlier spellings, both
	// still read. SLUIS_ is the one before the generic names; ACCESS_ROSTER_
	// the one before that.
	sluisEnvPrefix  = "SLUIS_"
	legacyEnvPrefix = "ACCESS_ROSTER_"
)

// legacySuffix maps a setting's suffix to its suffix under the earlier
// prefixes, for the one setting whose name changed beyond the prefix.
var legacySuffix = []struct{ now, was string }{{"ENDPOINT", "OTLP_ENDPOINT"}}

// envReader reads the extension's settings by their OTLP_LAMBDA_ names and
// falls back, in order, to the SLUIS_ and the ACCESS_ROSTER_ spellings. The
// newest name wins. It remembers each old name it had to use, never a value.
type envReader struct {
	getenv func(string) string
	legacy []string
}

func newEnvReader(getenv func(string) string) *envReader { return &envReader{getenv: getenv} }

// oldNames are the earlier spellings of name (an OTLP_LAMBDA_ name), newest
// first.
func oldNames(name string) []string {
	suffix := strings.TrimPrefix(name, envPrefix)
	for _, m := range legacySuffix {
		if m.now == suffix {
			suffix = m.was
			break
		}
	}
	return []string{sluisEnvPrefix + suffix, legacyEnvPrefix + suffix}
}

// currentName is the OTLP_LAMBDA_ name an old spelling was replaced by.
func currentName(old string) string {
	suffix := strings.TrimPrefix(strings.TrimPrefix(old, sluisEnvPrefix), legacyEnvPrefix)
	for _, m := range legacySuffix {
		if m.was == suffix {
			suffix = m.now
			break
		}
	}
	return envPrefix + suffix
}

// lookup returns the setting named name (an OTLP_LAMBDA_ name) or "" when no
// spelling holds a non-blank value.
func (e *envReader) lookup(name string) string {
	if v := e.getenv(name); strings.TrimSpace(v) != "" {
		return v
	}
	for _, old := range oldNames(name) {
		if v := e.getenv(old); strings.TrimSpace(v) != "" {
			if !slices.Contains(e.legacy, old) {
				e.legacy = append(e.legacy, old)
			}
			return v
		}
	}
	return ""
}

// deprecated is the old names that were read, sorted, one each.
func (e *envReader) deprecated() []string {
	out := slices.Clone(e.legacy)
	slices.Sort(out)
	return out
}

// warnDeprecated logs one line per old name used. It names the variables only.
func warnDeprecated(logf func(string, ...any), names []string) {
	if logf == nil {
		return
	}
	for _, old := range names {
		logf("otlp-lambda: %s is deprecated and still read; set %s instead", old, currentName(old))
	}
}
