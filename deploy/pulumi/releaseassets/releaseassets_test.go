package releaseassets_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/truvity/observability/deploy/pulumi/releaseassets"
)

var rel = releaseassets.Release{Repo: "truvity/example", Version: "1.2.3"}

// fake serves a checksums file and files; corrupt serves different bytes for
// "a.zip" than the checksums file lists.
func fake(files map[string][]byte, corrupt bool) releaseassets.Fetcher {
	var sums strings.Builder

	for name, b := range files {
		h := sha256.Sum256(b)
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(h[:]), name)
	}

	if corrupt {
		files["a.zip"] = []byte("tampered")
	}

	return func(_ context.Context, url string) ([]byte, error) {
		name, ok := strings.CutPrefix(url, rel.BaseURL())
		if !ok {
			return nil, fmt.Errorf("unexpected url %s", url)
		}

		if name == releaseassets.ChecksumsFile {
			return []byte(sums.String()), nil
		}

		if b, ok := files[name]; ok {
			return b, nil
		}

		return nil, fmt.Errorf("404 %s", name)
	}
}

func TestBaseURL(t *testing.T) {
	assert.Equal(t, "https://github.com/truvity/example/releases/download/v1.2.3/", rel.BaseURL())
}

func TestFetchVerifiesChecksums(t *testing.T) {
	files := map[string][]byte{"a.zip": []byte("a"), "b.zip": []byte("b")}

	got, err := releaseassets.Fetch(t.Context(), fake(files, false), t.TempDir(), rel, "a.zip", "b.zip")
	require.NoError(t, err)
	assert.Len(t, got, 2)
	assert.NotEmpty(t, got["a.zip"].SourceCodeHash)

	body, err := os.ReadFile(got["a.zip"].Path)
	require.NoError(t, err)
	assert.Equal(t, "a", string(body))

	_, err = releaseassets.Fetch(t.Context(), fake(map[string][]byte{"a.zip": []byte("a")}, true), t.TempDir(), rel, "a.zip")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match the release checksums file")
}

func TestFetchRefusesAnUnlistedAsset(t *testing.T) {
	_, err := releaseassets.Fetch(t.Context(), fake(map[string][]byte{"a.zip": []byte("a")}, false), t.TempDir(), rel, "other.zip")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no entry")
}

func TestFetchRefusesMissingChecksums(t *testing.T) {
	_, err := releaseassets.Fetch(t.Context(), func(context.Context, string) ([]byte, error) {
		return nil, fmt.Errorf("404")
	}, t.TempDir(), rel, "a.zip")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checksums")
}

func TestExtractAndHasMember(t *testing.T) {
	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)
	w, err := zw.Create("bootstrap")
	require.NoError(t, err)
	_, err = w.Write([]byte("binary"))
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	dir := t.TempDir()
	zp := filepath.Join(dir, "f.zip")
	require.NoError(t, os.WriteFile(zp, buf.Bytes(), 0o600))

	ok, err := releaseassets.HasMember(zp, "bootstrap")
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = releaseassets.HasMember(zp, "other")
	require.NoError(t, err)
	assert.False(t, ok)

	dest := filepath.Join(dir, "out", "bootstrap")
	require.NoError(t, releaseassets.Extract(zp, "bootstrap", dest, 0o755))

	info, err := os.Stat(dest)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())

	require.Error(t, releaseassets.Extract(zp, "other", filepath.Join(dir, "x"), 0o755))
}

func TestParseChecksumsStripsTheBinaryMarker(t *testing.T) {
	got := releaseassets.ParseChecksums([]byte("ABC  *a.zip\nnoise\n"))
	assert.Equal(t, map[string]string{"a.zip": "abc"}, got)
}
