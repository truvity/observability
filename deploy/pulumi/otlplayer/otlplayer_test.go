package otlplayer_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/truvity/observability/deploy/pulumi/otlplayer"
	"github.com/truvity/observability/deploy/pulumi/releaseassets"
)

func release(t *testing.T, member string) releaseassets.Fetcher {
	t.Helper()

	files := map[string][]byte{}

	var sums strings.Builder

	for _, arch := range []string{"arm64", "amd64"} {
		var buf bytes.Buffer

		zw := zip.NewWriter(&buf)
		w, err := zw.Create(member)
		require.NoError(t, err)
		_, err = w.Write([]byte("x"))
		require.NoError(t, err)
		require.NoError(t, zw.Close())

		name := otlplayer.AssetName("0.47.0", arch)
		files[name] = buf.Bytes()

		h := sha256.Sum256(buf.Bytes())
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(h[:]), name)
	}

	return func(_ context.Context, url string) ([]byte, error) {
		name, ok := strings.CutPrefix(url, "https://github.com/truvity/observability/releases/download/v0.47.0/")
		if !ok {
			return nil, fmt.Errorf("unexpected url %s", url)
		}

		if name == "checksums.txt" {
			return []byte(sums.String()), nil
		}

		if b, ok := files[name]; ok {
			return b, nil
		}

		return nil, fmt.Errorf("404 %s", name)
	}
}

var src = otlplayer.Source{Version: "0.47.0", Name: "otlp-lambda", Architectures: []string{"arm64", "amd64"}}

func TestAssetNameAndArch(t *testing.T) {
	assert.Equal(t, "otlp-lambda-layer_0.47.0_linux_arm64.zip", otlplayer.AssetName("0.47.0", "arm64"))
	assert.Equal(t, "x86_64", otlplayer.LambdaArch("amd64"))
	assert.Equal(t, "arm64", otlplayer.LambdaArch("arm64"))
}

func TestFetchChecksTheExtensionIsInTheZip(t *testing.T) {
	got, err := otlplayer.Fetch(t.Context(), release(t, otlplayer.ExtensionPath), t.TempDir(), src)
	require.NoError(t, err)
	assert.Len(t, got, 2)

	_, err = otlplayer.Fetch(t.Context(), release(t, "extensions/access-roster"), t.TempDir(), src)
	require.Error(t, err)
	assert.Contains(t, err.Error(), otlplayer.ExtensionPath)
}
