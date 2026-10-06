// Package releaseassets downloads files from a GitHub release of a public
// truvity repository and refuses any whose sha256 is not the one the release's
// own checksums file lists.
//
// It is the one place a stack fetches a release asset at deploy time: the
// OTLP Lambda layer (package otlplayer) and any other Lambda binary a
// release ships. Nothing is built here, and a file that is not in the checksums file is never used.
package releaseassets

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// ChecksumsFile is the name every truvity release gives its checksums.
	ChecksumsFile = "checksums.txt"

	maxAsset = 64 << 20
)

type (
	// Fetcher returns the body of a URL. Production uses HTTPS; tests inject
	// a map.
	Fetcher func(ctx context.Context, url string) ([]byte, error)

	// Release names one release of one repository.
	Release struct {
		// Repo is "owner/name", for example "truvity/observability".
		Repo string
		// Version is the release number without the leading v.
		Version string
	}

	// Asset is one verified release asset on local disk.
	Asset struct {
		// Path is the local file.
		Path string
		// SHA256 is the hex digest the checksums file lists.
		SHA256 string
		// SourceCodeHash is base64(sha256(file)), the form the AWS provider
		// keys a layer's or a function's replacement on.
		SourceCodeHash string
	}
)

// BaseURL is the directory a release's assets are downloaded from.
func (r Release) BaseURL() string {
	return "https://github.com/" + r.Repo + "/releases/download/v" + r.Version + "/"
}

// HTTPFetch is the production Fetcher.
func HTTPFetch(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("build request %s: %w", url, err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get %s: HTTP %d", url, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAsset))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", url, err)
	}

	return body, nil
}

// ParseChecksums reads a `sha256sum`-format file into name -> hex digest.
func ParseChecksums(data []byte) map[string]string {
	sums := map[string]string{}

	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 {
			sums[strings.TrimPrefix(f[1], "*")] = strings.ToLower(f[0])
		}
	}

	return sums
}

// Fetch downloads the release's checksums file and each named asset, refuses
// any asset whose sha256 is not the one the checksums file lists (or that the
// file does not list at all), and writes each verified asset under dir.
func Fetch(ctx context.Context, fetch Fetcher, dir string, rel Release, names ...string) (map[string]Asset, error) {
	base := rel.BaseURL()

	raw, err := fetch(ctx, base+ChecksumsFile)
	if err != nil {
		return nil, fmt.Errorf("download %s v%s checksums: %w", rel.Repo, rel.Version, err)
	}

	sums := ParseChecksums(raw)

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}

	out := make(map[string]Asset, len(names))

	for _, name := range names {
		want, ok := sums[name]
		if !ok {
			return nil, fmt.Errorf("%s release v%s checksums file has no entry for %s", rel.Repo, rel.Version, name)
		}

		body, err := fetch(ctx, base+name)
		if err != nil {
			return nil, fmt.Errorf("download %s: %w", name, err)
		}

		sum := sha256.Sum256(body)
		if got := hex.EncodeToString(sum[:]); got != want {
			return nil, fmt.Errorf("%s: sha256 %s does not match the release checksums file (%s): refusing to use it", name, got, want)
		}

		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, body, 0o600); err != nil {
			return nil, fmt.Errorf("write %s: %w", path, err)
		}

		out[name] = Asset{Path: path, SHA256: want, SourceCodeHash: base64.StdEncoding.EncodeToString(sum[:])}
	}

	return out, nil
}

// HasMember reports whether the zip holds a regular file at exactly this path.
func HasMember(zipPath, member string) (bool, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return false, fmt.Errorf("open %s: %w", zipPath, err)
	}
	defer func() { _ = zr.Close() }()

	for _, f := range zr.File {
		if f.Name == member && f.Mode().IsRegular() {
			return true, nil
		}
	}

	return false, nil
}

// Extract writes one member of a zip to dest with the given mode.
func Extract(zipPath, member, dest string, mode os.FileMode) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", zipPath, err)
	}
	defer func() { _ = zr.Close() }()

	for _, f := range zr.File {
		if f.Name != member {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("open %s in %s: %w", member, zipPath, err)
		}

		body, err := io.ReadAll(io.LimitReader(rc, maxAsset))
		_ = rc.Close()

		if err != nil {
			return fmt.Errorf("read %s in %s: %w", member, zipPath, err)
		}

		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(dest), err)
		}

		return os.WriteFile(dest, body, mode)
	}

	return errors.New(zipPath + " has no member " + member)
}
