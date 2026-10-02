package rulecheck

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ErrUnsupportedOS is returned when there are no release binaries for the
// running OS. A caller that would rather skip than fail can test for it.
var ErrUnsupportedOS = errors.New("no VictoriaMetrics release binaries for this OS")

type parser struct {
	repo, asset, binary string
}

var (
	metricsParser = parser{"VictoriaMetrics/VictoriaMetrics", "victoria-metrics", "victoria-metrics-prod"}
	logsParser    = parser{"VictoriaMetrics/VictoriaLogs", "victoria-logs", "victoria-logs-prod"}
)

// fetchMu serializes materializing binaries within the process. A fork in one
// goroutine while another holds a write fd on an executable makes the child
// inherit that fd, and exec of the file then fails with ETXTBSY
// (golang/go#22315); one writer at a time, plus the retry in startCmd, keeps
// parallel tests clear of it.
var fetchMu sync.Mutex

// binary returns the path to a release binary, downloading and caching it
// if needed.
func (p parser) fetch(ctx context.Context, o Options, version string) (string, error) {
	if o.BinDir != "" {
		return filepath.Join(o.BinDir, p.binary), nil
	}

	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return "", fmt.Errorf("%w: %s", ErrUnsupportedOS, runtime.GOOS)
	}

	cache := o.CacheDir
	if cache == "" {
		cache = filepath.Join(os.TempDir(), "rulecheck")
	}

	dir := filepath.Join(cache, version)
	path := filepath.Join(dir, p.binary)

	fetchMu.Lock()
	defer fetchMu.Unlock()

	if _, err := os.Stat(path); err == nil {
		return path, nil
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	tarball := fmt.Sprintf("%s-%s-%s-%s.tar.gz", p.asset, runtime.GOOS, runtime.GOARCH, version)
	base := fmt.Sprintf("https://github.com/%s/releases/download/%s/", p.repo, version)

	sums, err := httpGet(ctx, base+strings.TrimSuffix(tarball, ".tar.gz")+"_checksums.txt")
	if err != nil {
		return "", err
	}

	want := ""

	for line := range strings.SplitSeq(string(sums), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[1] == tarball {
			want = f[0]
		}
	}

	if want == "" {
		return "", fmt.Errorf("the release's checksum file lists no %s", tarball)
	}

	body, err := httpGet(ctx, base+tarball)
	if err != nil {
		return "", err
	}

	if sum := sha256.Sum256(body); hex.EncodeToString(sum[:]) != want {
		return "", fmt.Errorf("%s: sha256 %x does not match the release's published %s", tarball, sum, want)
	}

	gz, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("%s: %w", tarball, err)
	}

	tr := tar.NewReader(gz)

	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return "", fmt.Errorf("%s holds no %s", tarball, p.binary)
		}

		if err != nil {
			return "", fmt.Errorf("%s: %w", tarball, err)
		}

		if filepath.Base(h.Name) != p.binary {
			continue
		}

		bin, err := io.ReadAll(tr)
		if err != nil {
			return "", err
		}

		// Write beside, then rename: a concurrent run never sees half a
		// binary.
		tmp, err := os.CreateTemp(dir, p.binary+".*.tmp")
		if err != nil {
			return "", err
		}

		_, werr := tmp.Write(bin)
		serr := tmp.Sync()
		cerr := tmp.Close()

		if err := errors.Join(werr, serr, cerr, os.Chmod(tmp.Name(), 0o755), os.Rename(tmp.Name(), path)); err != nil {
			_ = os.Remove(tmp.Name())

			return "", err
		}

		return path, nil
	}
}

func httpGet(ctx context.Context, u string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", u, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: %s", u, resp.Status)
	}

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", u, err)
	}

	return b, nil
}
