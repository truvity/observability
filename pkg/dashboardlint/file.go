package dashboardlint

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LintFile reads one dashboard JSON file from disk and lints it. The
// dashboard's name, for findings, is its filename without extension.
func LintFile(path string) ([]Finding, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- a lint tool reads whatever files it is pointed at.
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	return Lint(name, raw)
}
