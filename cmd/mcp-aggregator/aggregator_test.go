package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fake is an in-process upstream: a stock-shaped MCP server (stateful, with
// sessions, resources and prompts, as the real ones have) whose tools are
// described by the test.
type fake struct {
	name    string
	srv     *httptest.Server
	mu      sync.Mutex
	calls   []string // raw arguments of every tools/call, in order
	started atomic.Int32
	// cancelled receives once when the `slow` tool sees its context end.
	cancelled chan struct{}
	sessions  []string // every Mcp-Session-Id the upstream issued
	// current is the MCP handler in service; restart swaps it for a fresh
	// one, which knows none of the sessions the old one issued.
	current atomic.Pointer[http.Handler]
	build   func() http.Handler
}

// restart simulates the upstream's pod restarting: every session it held is
// gone, and a request that names one is refused as the stock servers do.
func (f *fake) restart() { h := f.build(); f.current.Store(&h) }

var schemaQuery = map[string]any{
	"type":                 "object",
	"properties":           map[string]any{"query": map[string]any{"type": "string", "description": "the query"}},
	"required":             []any{"query"},
	"additionalProperties": false,
}

func newFake(t *testing.T, name string, tools ...string) *fake {
	t.Helper()
	return newFakeMode(t, false, name, tools...)
}

// newFakeMode builds a fake upstream. A stateful one keeps sessions, as the
// stock servers do in the 2025 revisions. A requestBound one is the shape the
// stock servers have in practice for cancellation: the tool handler lives
// exactly as long as the HTTP request that carries the call, so a caller that
// drops the connection cancels it.
func newFakeMode(t *testing.T, requestBound bool, name string, tools ...string) *fake {
	t.Helper()
	f := &fake{name: name, cancelled: make(chan struct{}, 4)}
	mk := func() *mcp.Server {
		s := mcp.NewServer(&mcp.Implementation{Name: name, Version: "1"}, &mcp.ServerOptions{
			Instructions: "upstream instructions that must not leak: read docs://" + name,
		})
		for _, tn := range tools {
			s.AddTool(&mcp.Tool{
				Name:        tn,
				Description: fmt.Sprintf("%s's %s tool. Verbatim text, with \"quotes\" and unicode: é.", name, tn),
				InputSchema: schemaQuery,
				Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
			}, f.handler(tn))
		}
		readme := func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{}, nil
		}
		s.AddResource(&mcp.Resource{URI: "docs://" + name + "/readme", Name: "readme"}, readme)
		s.AddPrompt(&mcp.Prompt{Name: "documentation"}, func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{}, nil
		})
		return s
	}
	opts := &mcp.StreamableHTTPOptions{Stateless: requestBound, PropagateRequestCancellation: requestBound}
	f.build = func() http.Handler {
		return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mk() }, opts)
	}
	f.restart()
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &sidRecorder{ResponseWriter: w, f: f}
		(*f.current.Load()).ServeHTTP(rec, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

type sidRecorder struct {
	http.ResponseWriter
	f *fake
}

func (s *sidRecorder) WriteHeader(code int) {
	if id := s.Header().Get("Mcp-Session-Id"); id != "" {
		s.f.mu.Lock()
		s.f.sessions = append(s.f.sessions, id)
		s.f.mu.Unlock()
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *sidRecorder) Write(b []byte) (int, error) {
	if id := s.Header().Get("Mcp-Session-Id"); id != "" {
		s.f.mu.Lock()
		s.f.sessions = append(s.f.sessions, id)
		s.f.mu.Unlock()
	}
	return s.ResponseWriter.Write(b)
}

func (s *sidRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (f *fake) handler(tool string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		f.mu.Lock()
		f.calls = append(f.calls, string(req.Params.Arguments))
		f.mu.Unlock()
		switch tool {
		case "slow":
			f.started.Add(1)
			<-ctx.Done()
			f.cancelled <- struct{}{}
			return nil, ctx.Err()
		case "fail":
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "upstream says no"}}}, nil
		}
		return &mcp.CallToolResult{
			Content:           []mcp.Content{&mcp.TextContent{Text: f.name + ":" + tool + ":" + string(req.Params.Arguments)}},
			StructuredContent: map[string]any{"backend": f.name, "tool": tool},
		}, nil
	}
}

func (f *fake) url() string { return f.srv.URL }

// harness runs an aggregator over the given backends and a client on it.
type harness struct {
	agg *Aggregator
	reg *prometheus.Registry
	srv *httptest.Server
}

func newHarness(t *testing.T, cfg *Config) *harness {
	t.Helper()
	reg := prometheus.NewRegistry()
	agg := NewAggregator(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), NewMetrics(reg))
	h := &harness{agg: agg, reg: reg, srv: httptest.NewServer(agg)}
	t.Cleanup(h.srv.Close)
	return h
}

func cfgFor(backends ...Backend) *Config {
	c := &Config{
		Instructions:     "store `primary`; cluster label k8s_cluster_name; tools are grouped by prefix.",
		AllowNonLoopback: true, // httptest binds 127.0.0.1, but keep the test independent of that
		Backends:         backends,
		CallTimeout:      Duration(5 * time.Second),
		ListTimeout:      Duration(5 * time.Second),
	}
	if err := c.applyDefaultsAndValidate(); err != nil {
		panic(err)
	}
	return c
}

func (h *harness) connect(t *testing.T, version string) *mcp.ClientSession {
	t.Helper()
	var opts *mcp.ClientSessionOptions
	if version != "" {
		opts = &mcp.ClientSessionOptions{ProtocolVersion: version}
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).
		Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: h.srv.URL, DisableStandaloneSSE: true}, opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func toolNames(t *testing.T, cs *mcp.ClientSession) []string {
	t.Helper()
	var names []string
	for tool, err := range cs.Tools(context.Background(), nil) {
		require.NoError(t, err)
		names = append(names, tool.Name)
	}
	return names
}

func counter(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			if labelsMatch(m, labels) {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func labelsMatch(m *dto.Metric, want map[string]string) bool {
	got := map[string]string{}
	for _, l := range m.GetLabel() {
		got[l.GetName()] = l.GetValue()
	}
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}

func textOf(t *testing.T, r *mcp.CallToolResult) string {
	t.Helper()
	require.NotEmpty(t, r.Content)
	tc, ok := r.Content[0].(*mcp.TextContent)
	require.True(t, ok, "content is %T", r.Content[0])
	return tc.Text
}

func TestPrefixingAndCollision(t *testing.T) {
	metrics := newFake(t, "m", "query", "labels")
	logs := newFake(t, "l", "query", "streams")
	h := newHarness(t, cfgFor(
		Backend{Prefix: "metrics", URL: metrics.url(), Tools: []string{"query", "labels"}},
		Backend{Prefix: "logs", URL: logs.url(), Tools: []string{"query", "streams"}},
	))
	require.NoError(t, h.agg.Sync(context.Background()))
	cs := h.connect(t, "")

	assert.ElementsMatch(t, []string{"metrics_query", "metrics_labels", "logs_query", "logs_streams"}, toolNames(t, cs))

	// The collision case: the same upstream name in two backends goes to
	// the right one each.
	r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "metrics_query", Arguments: map[string]any{"query": "up"}})
	require.NoError(t, err)
	assert.Equal(t, `m:query:{"query":"up"}`, textOf(t, r))
	r, err = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "logs_query", Arguments: map[string]any{"query": "error"}})
	require.NoError(t, err)
	assert.Equal(t, `l:query:{"query":"error"}`, textOf(t, r))
	assert.Len(t, metrics.calls, 1)
	assert.Len(t, logs.calls, 1)

	assert.Equal(t, 1.0, counter(t, h.reg, "mcp_aggregator_tool_calls_total", map[string]string{"tool": "logs_query", "outcome": "ok"}))
}

func TestAllowlistIsEnforced(t *testing.T) {
	up := newFake(t, "m", "query", "export", "delete_everything")
	h := newHarness(t, cfgFor(Backend{Prefix: "metrics", URL: up.url(), Tools: []string{"query"}}))
	require.NoError(t, h.agg.Sync(context.Background()))
	cs := h.connect(t, "")

	assert.Equal(t, []string{"metrics_query"}, toolNames(t, cs))

	// Neither the exposed spelling nor the upstream's own reaches a tool
	// that is not allowlisted.
	for _, name := range []string{"metrics_export", "export", "metrics_delete_everything", "query"} {
		_, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
		require.Error(t, err, name)
	}
	assert.Empty(t, up.calls, "a non-allowlisted tool reached the backend")
}

func TestMissingAllowlistedToolRefusesToBeReady(t *testing.T) {
	up := newFake(t, "m", "query")
	h := newHarness(t, cfgFor(Backend{Prefix: "metrics", URL: up.url(), Tools: []string{"query", "renamed_in_an_upgrade"}}))

	err := h.agg.Sync(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "renamed_in_an_upgrade")
	assert.False(t, h.agg.Ready())

	for _, path := range []string{"/"} {
		resp, err := http.Post(h.srv.URL+path, "application/json", strings.NewReader(`{}`))
		require.NoError(t, err)
		_ = resp.Body.Close()
		assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, "an unready aggregator must not serve a partial surface")
	}
	assert.Equal(t, 1.0, counter(t, h.reg, "mcp_aggregator_sync_failures_total", map[string]string{"backend": "metrics", "reason": "missing_tool"}))
}

func TestUnreachableBackendAtStartIsNotReadyThenRecovers(t *testing.T) {
	up := newFake(t, "m", "query")
	url := up.url()
	up.srv.Close()
	h := newHarness(t, cfgFor(Backend{Prefix: "metrics", URL: url, Tools: []string{"query"}}))
	require.Error(t, h.agg.Sync(context.Background()))
	assert.False(t, h.agg.Ready())
}

func TestResourcesAndPromptsAreRefused(t *testing.T) {
	up := newFake(t, "m", "query")
	h := newHarness(t, cfgFor(Backend{Prefix: "metrics", URL: up.url(), Tools: []string{"query"}}))
	require.NoError(t, h.agg.Sync(context.Background()))

	for _, version := range []string{"", "2025-06-18"} {
		cs := h.connect(t, version)
		init := cs.InitializeResult()
		require.NotNil(t, init.Capabilities)
		assert.NotNil(t, init.Capabilities.Tools, "tools is advertised")
		assert.False(t, init.Capabilities.Tools.ListChanged, "the set is static")
		assert.Nil(t, init.Capabilities.Resources, "resources must not be advertised")
		assert.Nil(t, init.Capabilities.Prompts, "prompts must not be advertised")
		assert.Nil(t, init.Capabilities.Completions)
		assert.Nil(t, init.Capabilities.Logging) //nolint:staticcheck // asserting it is NOT advertised

		ctx := context.Background()
		_, err := cs.ListResources(ctx, nil)
		assertMethodNotFound(t, err)
		_, err = cs.ListResourceTemplates(ctx, nil)
		assertMethodNotFound(t, err)
		_, err = cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "docs://m/readme"})
		assertMethodNotFound(t, err)
		_, err = cs.ListPrompts(ctx, nil)
		assertMethodNotFound(t, err)
		_, err = cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: "documentation"})
		assertMethodNotFound(t, err)
	}
}

func assertMethodNotFound(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	var je *jsonrpc.Error
	require.True(t, errors.As(err, &je), "%T: %v", err, err)
	assert.Equal(t, int64(jsonrpc.CodeMethodNotFound), int64(je.Code), err.Error())
}

func TestInstructionsAreOursNotTheUpstreams(t *testing.T) {
	up := newFake(t, "m", "query")
	cfg := cfgFor(Backend{Prefix: "metrics", URL: up.url(), Tools: []string{"query"}})
	h := newHarness(t, cfg)
	require.NoError(t, h.agg.Sync(context.Background()))
	cs := h.connect(t, "")
	assert.Equal(t, cfg.Instructions, cs.InitializeResult().Instructions)
	assert.NotContains(t, cs.InitializeResult().Instructions, "docs://")
}

func TestPassthroughIsVerbatim(t *testing.T) {
	up := newFake(t, "m", "query", "fail")
	h := newHarness(t, cfgFor(Backend{Prefix: "metrics", URL: up.url(), Tools: []string{"query", "fail"}}))
	require.NoError(t, h.agg.Sync(context.Background()))

	direct, err := mcp.NewClient(&mcp.Implementation{Name: "direct", Version: "1"}, nil).
		Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: up.url(), DisableStandaloneSSE: true}, nil)
	require.NoError(t, err)
	defer func() { _ = direct.Close() }()
	want := map[string]*mcp.Tool{}
	for tool, err := range direct.Tools(context.Background(), nil) {
		require.NoError(t, err)
		want[tool.Name] = tool
	}

	cs := h.connect(t, "")
	for tool, err := range cs.Tools(context.Background(), nil) {
		require.NoError(t, err)
		up := want[strings.TrimPrefix(tool.Name, "metrics_")]
		require.NotNil(t, up, tool.Name)
		assert.Equal(t, up.Description, tool.Description, "description is verbatim")
		assert.Equal(t, mustJSON(t, up.InputSchema), mustJSON(t, tool.InputSchema), "inputSchema is verbatim")
		assert.Equal(t, mustJSON(t, up.Annotations), mustJSON(t, tool.Annotations), "annotations are verbatim")
	}

	// Arguments reach the backend byte for byte: key order, numbers past
	// float64's precision, nesting, unicode.
	args := `{"query":"sum by (a) (rate(x{b=~\"é.*\"}[5m]))","big":12345678901234567890,"nested":{"z":[1,2,{"k":null}],"a":true}}`
	r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "metrics_query", Arguments: json.RawMessage(args)})
	require.NoError(t, err)
	require.Len(t, up.calls, 1)
	assert.JSONEq(t, args, up.calls[0])
	assert.Equal(t, "m:query:"+up.calls[0], textOf(t, r), "result content is the backend's")
	assert.Equal(t, map[string]any{"backend": "m", "tool": "query"}, r.StructuredContent)

	r, err = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "metrics_fail", Arguments: map[string]any{}})
	require.NoError(t, err)
	assert.True(t, r.IsError)
	assert.Equal(t, "upstream says no", textOf(t, r))
	assert.Equal(t, 1.0, counter(t, h.reg, "mcp_aggregator_tool_calls_total", map[string]string{"tool": "metrics_fail", "outcome": "tool_error"}))
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

func TestCancellationReachesTheBackend(t *testing.T) {
	up := newFakeMode(t, true, "m", "slow")
	h := newHarness(t, cfgFor(Backend{Prefix: "metrics", URL: up.url(), Tools: []string{"slow"}}))
	require.NoError(t, h.agg.Sync(context.Background()))
	cs := h.connect(t, "")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "metrics_slow", Arguments: map[string]any{}})
		done <- err
	}()
	require.Eventually(t, func() bool { return up.started.Load() == 1 }, 5*time.Second, 10*time.Millisecond, "the call never reached the backend")
	cancel()

	select {
	case <-up.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the backend never saw the caller's cancellation")
	}
	require.Error(t, <-done)
	require.Eventually(t, func() bool {
		return counter(t, h.reg, "mcp_aggregator_tool_calls_total", map[string]string{"tool": "metrics_slow", "outcome": "canceled"}) == 1
	}, 5*time.Second, 10*time.Millisecond)
}

func TestPerCallTimeout(t *testing.T) {
	up := newFakeMode(t, true, "m", "slow")
	cfg := cfgFor(Backend{Prefix: "metrics", URL: up.url(), Tools: []string{"slow"}})
	cfg.CallTimeout = Duration(300 * time.Millisecond)
	h := newHarness(t, cfg)
	require.NoError(t, h.agg.Sync(context.Background()))
	cs := h.connect(t, "")

	start := time.Now()
	r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "metrics_slow", Arguments: map[string]any{}})
	require.NoError(t, err)
	assert.Less(t, time.Since(start), 3*time.Second)
	assert.True(t, r.IsError)
	assert.Contains(t, textOf(t, r), "did not answer within")
	select {
	case <-up.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the timed-out call was left running on the backend")
	}
}

func TestPartialFailure(t *testing.T) {
	metrics := newFake(t, "m", "query")
	logs := newFake(t, "l", "query")
	h := newHarness(t, cfgFor(
		Backend{Prefix: "metrics", URL: metrics.url(), Tools: []string{"query"}},
		Backend{Prefix: "logs", URL: logs.url(), Tools: []string{"query"}},
	))
	require.NoError(t, h.agg.Sync(context.Background()))
	cs := h.connect(t, "")

	logs.srv.Close() // the backend goes away after start

	// The surface is static: the tools are still listed.
	assert.ElementsMatch(t, []string{"metrics_query", "logs_query"}, toolNames(t, cs))
	assert.True(t, h.agg.Ready(), "a backend that goes down later must not make the pod unready")

	r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "logs_query", Arguments: map[string]any{"query": "x"}})
	require.NoError(t, err, "a dead backend is a tool error the model can read, not a protocol error")
	assert.True(t, r.IsError)
	assert.Contains(t, textOf(t, r), "logs backend is unavailable")
	assert.Contains(t, textOf(t, r), "other backends are unaffected")

	// And the other backend is untouched.
	r, err = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "metrics_query", Arguments: map[string]any{"query": "x"}})
	require.NoError(t, err)
	assert.False(t, r.IsError)
	assert.Equal(t, 1.0, counter(t, h.reg, "mcp_aggregator_tool_calls_total", map[string]string{"tool": "logs_query", "outcome": "unavailable"}))
}

func TestUpstreamSessionsNeverReachTheClient(t *testing.T) {
	up := newFake(t, "m", "query")
	h := newHarness(t, cfgFor(Backend{Prefix: "metrics", URL: up.url(), Tools: []string{"query"}}))
	require.NoError(t, h.agg.Sync(context.Background()))

	post := func(body string, hdr map[string]string) (*http.Response, string) {
		req, _ := http.NewRequest(http.MethodPost, h.srv.URL, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}

	var wire []string
	var headers []http.Header
	for _, step := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"c","version":"1"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"metrics_query","arguments":{"query":"up"}}}`,
	} {
		resp, body := post(step, map[string]string{"Mcp-Protocol-Version": "2025-06-18"})
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
		wire = append(wire, body)
		headers = append(headers, resp.Header)
	}

	require.NotEmpty(t, up.sessions, "the fake upstream issued no session: the test proves nothing")
	for i, hdr := range headers {
		assert.Empty(t, hdr.Get("Mcp-Session-Id"), "step %d minted a session id", i)
		for _, sid := range up.sessions {
			assert.NotContains(t, wire[i], sid, "step %d leaked an upstream session id in the body", i)
			for k, v := range hdr {
				assert.NotContains(t, strings.Join(v, ","), sid, "step %d leaked an upstream session id in %s", i, k)
			}
		}
	}
	// The legacy handshake works with no session at all, and the call saw
	// the tool's real answer.
	assert.Contains(t, wire[2], `m:query:`)
}

func TestBothProtocolDialects(t *testing.T) {
	up := newFake(t, "m", "query")
	h := newHarness(t, cfgFor(Backend{Prefix: "metrics", URL: up.url(), Tools: []string{"query"}}))
	require.NoError(t, h.agg.Sync(context.Background()))

	for version, want := range map[string]string{"": "2026-07-28", "2025-06-18": "2025-06-18", "2025-11-25": "2025-11-25"} {
		cs := h.connect(t, version)
		assert.Equal(t, want, cs.InitializeResult().ProtocolVersion, "client asked for %q", version)
		assert.Empty(t, cs.ID(), "no session id in any dialect")
		r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "metrics_query", Arguments: map[string]any{"query": "up"}})
		require.NoError(t, err)
		assert.False(t, r.IsError)
	}
}

func TestAuthorizerHook(t *testing.T) {
	up := newFake(t, "m", "query", "labels")
	h := newHarness(t, cfgFor(Backend{Prefix: "metrics", URL: up.url(), Tools: []string{"query", "labels"}}))
	var seen []string
	h.agg.Authorize = func(_ context.Context, tool string, _ *mcp.CallToolRequest) error {
		seen = append(seen, tool)
		if tool == "metrics_labels" {
			return errors.New("not for you")
		}
		return nil
	}
	require.NoError(t, h.agg.Sync(context.Background()))
	cs := h.connect(t, "")

	r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "metrics_labels", Arguments: map[string]any{}})
	require.NoError(t, err)
	assert.True(t, r.IsError)
	assert.Contains(t, textOf(t, r), "not permitted")
	assert.Empty(t, up.calls, "a denied call reached the backend")
	_, err = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "metrics_query", Arguments: map[string]any{}})
	require.NoError(t, err)
	assert.Equal(t, []string{"metrics_labels", "metrics_query"}, seen)
}

func TestMetricsNeverCarryArguments(t *testing.T) {
	up := newFake(t, "m", "query")
	h := newHarness(t, cfgFor(Backend{Prefix: "metrics", URL: up.url(), Tools: []string{"query"}}))
	require.NoError(t, h.agg.Sync(context.Background()))
	cs := h.connect(t, "")
	_, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "metrics_query", Arguments: map[string]any{"query": "SECRET-ARGUMENT"}})
	require.NoError(t, err)

	mfs, err := h.reg.Gather()
	require.NoError(t, err)
	var buf bytes.Buffer
	for _, mf := range mfs {
		buf.WriteString(mf.String())
	}
	assert.NotContains(t, buf.String(), "SECRET-ARGUMENT")
}

// TestClientSessionSurvivesRestart pins the restart story: nothing in the pod
// holds MCP session state, so a client that holds a session id from before a
// restart, and an upstream that restarted and forgot every session, change
// nothing for a call.
func TestClientSessionSurvivesRestart(t *testing.T) {
	up := newFake(t, "m", "query")
	h := newHarness(t, cfgFor(Backend{Prefix: "metrics", URL: up.url(), Tools: []string{"query"}}))
	require.NoError(t, h.agg.Sync(context.Background()))

	post := func(body string, hdr map[string]string) (*http.Response, string) {
		req, _ := http.NewRequest(http.MethodPost, h.srv.URL, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}
	// What a connector sends after a restart: a session id from the old pod,
	// on a request that is not an initialize.
	stale := map[string]string{"Mcp-Session-Id": "id-from-the-pod-that-was-replaced", "Mcp-Protocol-Version": "2025-06-18"}
	for name, body := range map[string]string{
		"tools/list": `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		"tools/call": `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"metrics_query","arguments":{"query":"up"}}}`,
	} {
		resp, out := post(body, stale)
		assert.Equal(t, http.StatusOK, resp.StatusCode, "%s with a stale session id: %s", name, out)
		assert.Contains(t, out, `"result"`, name)
		assert.Empty(t, resp.Header.Get("Mcp-Session-Id"), name)
	}

	// An upstream that restarts between two calls: the next call opens a new
	// upstream session instead of reusing a dead one.
	cs := h.connect(t, "")
	call := func() {
		t.Helper()
		r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "metrics_query", Arguments: map[string]any{"query": "up"}})
		require.NoError(t, err)
		assert.False(t, r.IsError, "%v", r.Content)
	}
	call()
	up.restart()
	call()
}
