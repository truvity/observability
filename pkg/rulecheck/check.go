package rulecheck

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Options say which parsers to run.
type Options struct {
	// VMVersion and VLVersion are the release tags (vX.Y.Z) of
	// VictoriaMetrics and VictoriaLogs. Required unless BinDir is set and
	// the caller does not need them named in a finding.
	VMVersion, VLVersion string
	// BinDir, when set, names a directory already holding
	// `victoria-metrics-prod` and `victoria-logs-prod`; nothing is
	// downloaded.
	BinDir string
	// CacheDir is where downloaded binaries are kept, one directory per
	// version. Empty means a directory under the OS temp dir.
	CacheDir string
}

// Finding is one expression a parser refused.
type Finding struct {
	Rule
	// Parser is "victoria-metrics" or "victoria-logs"; Version its tag.
	Parser, Version string
	// Err is the parser's own answer.
	Err string
}

func (f Finding) String() string {
	return fmt.Sprintf("invalid expression\n  %s\n  parser (%s %s): %s\n  expr: %s", f.Rule, f.Parser, f.Version, f.Err, f.Expr)
}

// ErrNoRules is returned when there is nothing to check: a gate that
// checks nothing passes whatever the rules say.
var ErrNoRules = errors.New("no VMRule expression found -- has the layout moved? (this check would otherwise pass while checking nothing)")

// Check parses every rule with the real parser for its type and returns the
// expressions that parser refused. An error is a failure to run the check
// (no rules, no binary, a parser that will not start), never a finding.
func Check(ctx context.Context, rules []Rule, o Options) ([]Finding, error) {
	if len(rules) == 0 {
		return nil, ErrNoRules
	}

	var (
		findings      []Finding
		metrics, logs *running
	)

	defer func() {
		metrics.stop()
		logs.stop()
	}()

	now := fmt.Sprint(time.Now().Unix())

	for _, r := range rules {
		var (
			p    parser
			ver  string
			slot **running
			path string
		)

		if r.LogsQL() {
			p, ver, slot, path = logsParser, o.VLVersion, &logs, "/select/logsql/stats_query"
		} else {
			p, ver, slot, path = metricsParser, o.VMVersion, &metrics, "/api/v1/query"
		}

		if *slot == nil {
			bin, err := p.fetch(ctx, o, ver)
			if err != nil {
				return nil, err
			}

			if *slot, err = start(ctx, bin); err != nil {
				return nil, err
			}
		}

		if msg := post(ctx, (*slot).base+path, url.Values{"query": {r.Expr}, "time": {now}}); msg != "" {
			findings = append(findings, Finding{Rule: r, Parser: strings.TrimSuffix(p.binary, "-prod"), Version: ver, Err: msg})
		}
	}

	return findings, nil
}

// running is a parser process on a loopback port.
type running struct {
	base   string
	cancel context.CancelFunc
	cmd    *exec.Cmd
	done   chan struct{}
	dir    string
}

func (r *running) stop() {
	if r == nil {
		return
	}

	r.cancel()
	<-r.done
	_ = r.cmd.Wait()
	_ = os.RemoveAll(r.dir)
}

// start runs a binary on a free loopback port with a scratch data dir and
// waits for it to be healthy.
func start(ctx context.Context, bin string) (*running, error) {
	l, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}

	addr := l.Addr().String()
	_ = l.Close()

	dir, err := os.MkdirTemp("", "rulecheck-data-")
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(ctx)

	cmd, stderr, err := startCmd(ctx, bin, "-httpListenAddr="+addr, "-storageDataPath="+dir, "-loggerLevel=ERROR")
	if err != nil {
		cancel()
		_ = os.RemoveAll(dir)

		return nil, fmt.Errorf("start %s: %w", bin, err)
	}

	var tail strings.Builder

	r := &running{base: "http://" + addr, cancel: cancel, cmd: cmd, done: make(chan struct{}), dir: dir}

	go func() {
		defer close(r.done)

		sc := bufio.NewScanner(stderr)
		for sc.Scan() && tail.Len() < 4096 {
			tail.WriteString(sc.Text() + "\n")
		}
	}()

	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, r.base+"/health", nil)

		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()

			if resp.StatusCode == http.StatusOK {
				return r, nil
			}
		}
	}

	r.stop()

	return nil, fmt.Errorf("%s did not become healthy on %s:\n%s", filepath.Base(bin), addr, tail.String())
}

// startCmd starts bin with stdout discarded and returns its stderr pipe. A
// start that fails with ETXTBSY is retried a few times: another goroutine's
// fork can briefly hold a write fd on a binary just materialized
// (golang/go#22315), and the fd closes as soon as that child execs.
func startCmd(ctx context.Context, bin string, args ...string) (*exec.Cmd, io.ReadCloser, error) {
	const tries = 5

	for attempt := 1; ; attempt++ {
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Stdout = io.Discard

		stderr, err := cmd.StderrPipe()
		if err == nil {
			if err = cmd.Start(); err == nil {
				return cmd, stderr, nil
			}
		}

		if !errors.Is(err, syscall.ETXTBSY) || attempt == tries {
			return nil, nil, err
		}

		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(time.Duration(attempt) * 50 * time.Millisecond):
		}
	}
}

// post sends one expression; "" means the parser accepted it.
func post(ctx context.Context, target string, form url.Values) string {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return "request failed: " + err.Error()
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "request failed: " + err.Error()
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode == http.StatusOK {
		return ""
	}

	return fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
}
