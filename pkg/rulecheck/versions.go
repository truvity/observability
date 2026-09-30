package rulecheck

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Versions are the release tags of the two parsers.
type Versions struct{ Metrics, Logs string }

// ChartVersions reads the parser versions out of the vendored dependency
// archives of charts/observability-stack (its `charts/` directory): the
// appVersion of victoria-metrics-k8s-stack and of victoria-logs-single,
// which are the tags the stack's VMSingle and victoria-logs run. The
// parsers therefore follow the chart, and no tag is written down twice.
func ChartVersions(stackChartsDir string) (Versions, error) {
	m, err := archiveAppVersion(stackChartsDir, "victoria-metrics-k8s-stack")
	if err != nil {
		return Versions{}, err
	}

	l, err := archiveAppVersion(stackChartsDir, "victoria-logs-single")
	if err != nil {
		return Versions{}, err
	}

	return Versions{Metrics: m, Logs: l}, nil
}

var versionRE = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

func archiveAppVersion(dir, chart string) (string, error) {
	matches, _ := filepath.Glob(filepath.Join(dir, chart+"-*.tgz"))
	if len(matches) == 0 {
		return "", fmt.Errorf("no %s-*.tgz in %s -- run the vendor recipe", chart, dir)
	}

	f, err := os.Open(matches[0])
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("%s: %w", matches[0], err)
	}

	tr := tar.NewReader(gz)

	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return "", fmt.Errorf("%s holds no %s/Chart.yaml", matches[0], chart)
		}

		if err != nil {
			return "", fmt.Errorf("%s: %w", matches[0], err)
		}

		if h.Name != chart+"/Chart.yaml" {
			continue
		}

		var c struct {
			AppVersion string `yaml:"appVersion"`
		}

		if err := yaml.NewDecoder(tr).Decode(&c); err != nil {
			return "", fmt.Errorf("%s: %w", matches[0], err)
		}

		if !versionRE.MatchString(c.AppVersion) {
			return "", fmt.Errorf("%s: appVersion %q is not vX.Y.Z", matches[0], c.AppVersion)
		}

		return c.AppVersion, nil
	}
}

var logsImageRE = regexp.MustCompile(`victoriametrics/victoria-logs:(v[0-9][0-9.]*)`)

// RenderedVersions reads the parser versions out of a rendered
// observability-stack manifest: the VMSingle's spec.image.tag and the
// victoria-logs image. For a consumer that renders the published chart at
// its own pin and wants to prove its parser tags have not drifted.
func RenderedVersions(rendered []byte) (Versions, error) {
	logs := logsImageRE.FindSubmatch(rendered)
	if logs == nil {
		return Versions{}, errors.New("the rendered chart has no victoriametrics/victoria-logs image")
	}

	dec := yaml.NewDecoder(strings.NewReader(string(rendered)))

	for {
		var doc struct {
			Kind string `yaml:"kind"`
			Spec struct {
				Image struct {
					Tag string `yaml:"tag"`
				} `yaml:"image"`
			} `yaml:"spec"`
		}

		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return Versions{}, errors.New("the rendered chart has no VMSingle with spec.image.tag")
		}

		if err != nil {
			return Versions{}, fmt.Errorf("parse rendered chart: %w", err)
		}

		if doc.Kind == "VMSingle" && doc.Spec.Image.Tag != "" {
			return Versions{Metrics: doc.Spec.Image.Tag, Logs: string(logs[1])}, nil
		}
	}
}
