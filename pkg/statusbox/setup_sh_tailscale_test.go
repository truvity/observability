package statusbox_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSetupTailscaleUpFlags proves setup.sh's setup_tailscale function
// calls `tailscale up` with the exact flags this box's network design
// depends on:
//
//   - --accept-routes: without it, a subnet router elsewhere on the
//     tailnet can advertise all it wants and this box never installs the
//     resulting route, so a private IP has no path off the box.
//   - --accept-dns=true: without it, MagicDNS and whatever split-DNS
//     routes the tailnet admin delegated to a resolver behind that same
//     subnet router never reach this box, so a private name never
//     resolves in the first place.
//
// and never --advertise-routes or an exit-node flag: this box is a
// tailnet CLIENT, never a router for anyone else.
//
// setup.sh is a release asset this package never imports or embeds — it
// is fetched and run on a real box by cloud-init, verified by checksum
// (see CloudInit's own doc comment) — so this test runs it directly,
// unmodified, from the repository root, with a stub `tailscale` binary
// on PATH standing in for the real CLI: `command -v tailscale` finds the
// stub, `tailscale status` reports "not joined" (exit 1) so
// setup_tailscale reaches the `up` call, and `tailscale up ...` records
// its own argv to a file instead of touching any real network.
func TestSetupTailscaleUpFlags(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller must resolve this test file's own path")
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	setupSh := filepath.Join(repoRoot, "setup.sh")
	_, err := os.Stat(setupSh)
	require.NoError(t, err, "setup.sh must exist at the repository root")

	stubDir := t.TempDir()
	argvFile := filepath.Join(stubDir, "tailscale-up-argv")

	// The stub only implements the two subcommands setup_tailscale ever
	// calls: `status`, refused so the function falls through to `up`,
	// and `up`, which records every argument it was given — one per
	// line, so a flag carrying "=" is not mistaken for a delimiter — and
	// nothing else.
	stub := `#!/bin/sh
set -eu
case "$1" in
  status)
    exit 1
    ;;
  up)
    shift
    : > "$ARGV_FILE"
    for a in "$@"; do printf '%s\n' "$a" >> "$ARGV_FILE"; done
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
`
	stubPath := filepath.Join(stubDir, "tailscale")
	require.NoError(t, os.WriteFile(stubPath, []byte(stub), 0o755))

	cmd := exec.Command("bash", setupSh, "setup_tailscale")
	cmd.Env = append(os.Environ(),
		"PATH="+stubDir+":"+os.Getenv("PATH"),
		"ARGV_FILE="+argvFile,
		"TS_AUTHKEY=test-authkey",
		"TS_HOSTNAME=test-statusbox",
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "setup.sh setup_tailscale failed:\n%s", out)

	got, err := os.ReadFile(argvFile)
	require.NoError(t, err, "the stub tailscale binary's 'up' subcommand was never invoked:\n%s", out)

	raw := strings.TrimRight(string(got), "\n")
	require.NotEmpty(t, raw, "tailscale up recorded no arguments at all")
	args := strings.Split(raw, "\n")

	require.Contains(t, args, "--authkey=test-authkey")
	require.Contains(t, args, "--hostname=test-statusbox")
	require.Contains(t, args, "--ssh")
	require.Contains(t, args, "--accept-routes",
		"without --accept-routes this box never installs a subnet router's advertised route, and every private IP is unreachable")
	require.Contains(t, args, "--accept-dns=true",
		"without --accept-dns=true this box never picks up MagicDNS or the tailnet's split-DNS routes, and every private name fails to resolve")

	for _, a := range args {
		require.NotEqual(t, "--accept-dns=false", a,
			"this box must accept the tailnet's own DNS, not refuse it")
		require.False(t, strings.HasPrefix(a, "--advertise-routes"),
			"this box must never advertise routes of its own — accepting routes is client-side only: %s", a)
		require.NotEqual(t, "--advertise-exit-node", a,
			"this box must never offer itself as an exit node")
		require.False(t, strings.Contains(a, "--exit-node"),
			"this box must never adopt or advertise an exit node: %s", a)
	}
}
