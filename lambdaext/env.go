package lambdaext

import (
	"slices"
	"strings"
)

const (
	envPrefix       = "SLUIS_"
	legacyEnvPrefix = "ACCESS_ROSTER_"
)

// envReader reads the extension's settings by their SLUIS_ names and falls back
// to the ACCESS_ROSTER_ spelling the extension read before the rename. The new
// name wins. It remembers each old name it had to use, never a value.
type envReader struct {
	getenv func(string) string
	legacy []string
}

func newEnvReader(getenv func(string) string) *envReader { return &envReader{getenv: getenv} }

// lookup returns the setting named name (a SLUIS_ name) or "" when neither
// spelling holds a non-blank value.
func (e *envReader) lookup(name string) string {
	if v := e.getenv(name); strings.TrimSpace(v) != "" {
		return v
	}
	old := legacyEnvPrefix + strings.TrimPrefix(name, envPrefix)
	if v := e.getenv(old); strings.TrimSpace(v) != "" {
		if !slices.Contains(e.legacy, old) {
			e.legacy = append(e.legacy, old)
		}
		return v
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
		logf("otlp-lambda: %s is deprecated and still read; set %s%s instead",
			old, envPrefix, strings.TrimPrefix(old, legacyEnvPrefix))
	}
}
