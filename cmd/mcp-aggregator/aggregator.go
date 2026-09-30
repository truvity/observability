package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Authorizer is the per-tool authorization hook. It runs before every
// tools/call, with the exposed (prefixed) tool name, and refuses the call by
// returning an error. It is UNUSED today: every role that reaches this
// server reads every store, and who may reach it at all is decided by the
// resource-proxy in front. The hook exists so that the day a tool needs a
// narrower audience, the decision has one place to live that is not a
// client's good manners.
type Authorizer func(ctx context.Context, tool string, req *mcp.CallToolRequest) error

// protocolVersion20260728 is the first revision with no handshake. A backend
// that negotiated anything older is called with the same revision the next
// time, which skips a probe it is known to refuse.
const protocolVersion20260728 = "2026-07-28"

type route struct {
	backend  *backend
	upstream string
}

// snapshot is the static surface: built once at start from the backends'
// own tools/list, filtered by the allowlist, and served until the process
// ends. A backend that later changes its mind changes nothing here.
type snapshot struct {
	tools  []*mcp.Tool
	routes map[string]route
}

type backend struct {
	Backend
	version atomic.Value // string: the revision negotiated at the last connect
}

// Aggregator serves the union of its backends' allowlisted tools.
type Aggregator struct {
	cfg      *Config
	log      *slog.Logger
	metrics  *Metrics
	backends []*backend
	// Authorize is the per-tool hook; nil permits every call.
	Authorize Authorizer
	// HTTPClient is used to reach the backends.
	HTTPClient *http.Client
	// Version is advertised as the server's version.
	Version string

	ready   atomic.Bool
	handler atomic.Pointer[http.Handler]
	mu      sync.Mutex // serialises Sync
}

// NewAggregator builds an aggregator for a validated configuration.
func NewAggregator(cfg *Config, log *slog.Logger, m *Metrics) *Aggregator {
	a := &Aggregator{
		cfg:        cfg,
		log:        log,
		metrics:    m,
		HTTPClient: &http.Client{},
		Version:    "dev",
	}
	for _, b := range cfg.Backends {
		a.backends = append(a.backends, &backend{Backend: b})
	}
	return a
}

// Ready reports whether the snapshot was built, i.e. every allowlisted tool
// was found. It never goes false again: a backend that goes down later
// fails its own calls, not the whole surface.
func (a *Aggregator) Ready() bool { return a.ready.Load() }

func (a *Aggregator) connect(ctx context.Context, b *backend) (*mcp.ClientSession, error) {
	c := mcp.NewClient(&mcp.Implementation{Name: a.cfg.ServerName + "-aggregator", Version: a.Version}, nil)
	var opts *mcp.ClientSessionOptions
	if v, _ := b.version.Load().(string); v != "" && v < protocolVersion20260728 {
		opts = &mcp.ClientSessionOptions{ProtocolVersion: v}
	}
	cs, err := c.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             b.URL,
		HTTPClient:           a.HTTPClient,
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}, opts)
	if err != nil {
		return nil, err
	}
	if r := cs.InitializeResult(); r != nil {
		b.version.Store(r.ProtocolVersion)
	}
	return cs, nil
}

// Sync asks every backend for its tools, keeps the allowlisted ones and,
// only if none is missing, starts serving them. It returns one error naming
// everything that is wrong, so a single attempt tells an operator the whole
// story.
func (a *Aggregator) Sync(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	snap := &snapshot{routes: map[string]route{}}
	var problems []string
	for _, b := range a.backends {
		tools, err := a.listBackend(ctx, b)
		if err != nil {
			a.metrics.SyncFailures.WithLabelValues(b.Prefix, "unreachable").Inc()
			problems = append(problems, fmt.Sprintf("backend %q is unreachable: %v", b.Prefix, err))
			continue
		}
		byName := map[string]*mcp.Tool{}
		for _, t := range tools {
			byName[t.Name] = t
		}
		var missing []string
		for _, name := range b.Tools {
			t, ok := byName[name]
			if !ok {
				missing = append(missing, name)
				continue
			}
			exposed := *t
			exposed.Name = b.Prefix + "_" + name
			snap.tools = append(snap.tools, &exposed)
			snap.routes[exposed.Name] = route{backend: b, upstream: name}
			delete(byName, name)
		}
		var unlisted []string
		for name := range byName {
			unlisted = append(unlisted, name)
		}
		sort.Strings(unlisted)
		a.metrics.Unlisted.WithLabelValues(b.Prefix).Set(float64(len(unlisted)))
		if len(unlisted) > 0 {
			// Not exposed, and said so: an upgrade that adds a tool is news,
			// but never a surface change.
			a.log.Warn("backend offers tools that are not allowlisted; they are not exposed",
				"backend", b.Prefix, "tools", unlisted)
		}
		if len(missing) > 0 {
			a.metrics.SyncFailures.WithLabelValues(b.Prefix, "missing_tool").Inc()
			sort.Strings(missing)
			problems = append(problems, fmt.Sprintf("backend %q does not offer allowlisted tools %s", b.Prefix, strings.Join(missing, ", ")))
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}

	srv := a.newServer(snap)
	var h http.Handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		// Stateless: no Mcp-Session-Id is ever minted, which is what lets one
		// process answer both the 2026-07-28 revision (which has no sessions)
		// and the older handshake, and lets any replica answer any request.
		//
		// PropagateRequestCancellation ties a 2026-07-28 call to its HTTP
		// request: a client that gives up (closes the request) cancels the
		// backend call. The older revisions cancel with a notification that a
		// stateless server has no session to deliver to; for them the
		// per-call timeout is what bounds a call nobody is waiting for.
		&mcp.StreamableHTTPOptions{Stateless: true, PropagateRequestCancellation: true})
	a.handler.Store(&h)
	a.metrics.Tools.Set(float64(len(snap.tools)))
	a.metrics.Ready.Set(1)
	a.ready.Store(true)
	return nil
}

func (a *Aggregator) listBackend(ctx context.Context, b *backend) ([]*mcp.Tool, error) {
	ctx, cancel := context.WithTimeout(ctx, a.cfg.ListTimeout.Std())
	defer cancel()
	cs, err := a.connect(ctx, b)
	if err != nil {
		return nil, err
	}
	defer func() { _ = cs.Close() }()
	var out []*mcp.Tool
	for t, err := range cs.Tools(ctx, nil) {
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// Run retries Sync until it succeeds or ctx ends. Not being ready is the
// honest answer until then: the pod is not sent traffic, and the log says
// what is wrong.
func (a *Aggregator) Run(ctx context.Context) {
	delay := time.Second
	for {
		err := a.Sync(ctx)
		if err == nil {
			a.log.Info("ready")
			return
		}
		a.log.Error("not ready", "error", err.Error(), "retry_in", delay.String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if delay < 15*time.Second {
			delay *= 2
		}
	}
}

// ServeHTTP serves the MCP endpoint, or 503 until the surface is built.
func (a *Aggregator) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := a.handler.Load()
	if h == nil {
		http.Error(w, "not ready: the tool surface has not been built", http.StatusServiceUnavailable)
		return
	}
	(*h).ServeHTTP(w, r)
}

// allowedMethods is everything this server answers. The rest, resources/*,
// prompts/*, completion/complete, logging/setLevel and the subscription
// methods, is refused as not found: the capability is not advertised, and a
// stock backend's documentation resources (thousands of them, colliding
// across backends) must never reach a client.
var allowedMethods = map[string]bool{
	"initialize":                true,
	"server/discover":           true,
	"ping":                      true,
	"tools/list":                true,
	"tools/call":                true,
	"subscriptions/listen":      true,
	"notifications/initialized": true,
	"notifications/cancelled":   true,
	"notifications/progress":    true,
}

func toolsOnly(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if !allowedMethods[method] {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: fmt.Sprintf("method %q is not supported: this server offers tools only", method)}
		}
		return next(ctx, method, req)
	}
}

func (a *Aggregator) newServer(snap *snapshot) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: a.cfg.ServerName, Version: a.Version}, &mcp.ServerOptions{
		Instructions: a.cfg.Instructions,
		// tools, and only tools; listChanged is false because the set is static.
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
	})
	srv.AddReceivingMiddleware(toolsOnly)
	for _, t := range snap.tools {
		srv.AddTool(t, a.toolHandler(snap.routes[t.Name], t.Name))
	}
	return srv
}

func (a *Aggregator) toolHandler(rt route, exposed string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		start := time.Now()
		outcome := "ok"
		defer func() {
			a.metrics.Calls.WithLabelValues(exposed, outcome).Inc()
			a.metrics.Duration.WithLabelValues(exposed).Observe(time.Since(start).Seconds())
			// Name, backend, outcome and duration only: never the arguments
			// or the result.
			a.log.Info("tools/call", "tool", exposed, "backend", rt.backend.Prefix, "outcome", outcome, "duration_ms", time.Since(start).Milliseconds())
		}()

		if a.Authorize != nil {
			if err := a.Authorize(ctx, exposed, req); err != nil {
				outcome = "denied"
				return errorResult("call to %s is not permitted: %v", exposed, err), nil
			}
		}

		callerCtx := ctx
		ctx, cancel := upstreamContext(callerCtx, a.cfg.CallTimeout.Std())
		defer cancel()

		// One upstream session per call, closed when the call ends. No
		// session survives a call, so none can go stale, and none is ever
		// visible to the client.
		cs, err := a.connect(ctx, rt.backend)
		if err != nil {
			return a.failed(callerCtx, ctx, rt, &outcome, "unavailable", err), nil
		}
		defer func() { _ = cs.Close() }()

		var args any
		if len(req.Params.Arguments) > 0 {
			args = req.Params.Arguments
		}
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: rt.upstream, Arguments: args})
		if err != nil {
			kind := "upstream_error"
			var je *jsonrpc.Error
			if !errors.As(err, &je) {
				kind = "unavailable"
			}
			return a.failed(callerCtx, ctx, rt, &outcome, kind, err), nil
		}
		if res.IsError {
			outcome = "tool_error"
		}
		return res, nil
	}
}

// upstreamContext is the context of one backend call: it ends when the
// caller's does (a cancelled or dropped request cancels the backend call) or
// when the per-call timeout passes.
//
// It is deliberately NOT derived from the caller's context. The SDK's
// server transport stamps the protocol revision of the INBOUND request into
// that context, and its client transport reads the same key to choose the
// Mcp-Protocol-Version header of an OUTBOUND request: a call derived from
// it would speak the caller's revision to a backend that has not been asked
// whether it does. Only the cancellation is carried over.
func upstreamContext(caller context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	stop := context.AfterFunc(caller, cancel)
	return ctx, func() { stop(); cancel() }
}

// failed turns a transport or protocol failure into a tool error the model
// can read and report, and names the backend so "logs are down" is
// distinguishable from "the query was wrong".
func (a *Aggregator) failed(callerCtx, ctx context.Context, rt route, outcome *string, kind string, err error) *mcp.CallToolResult {
	switch {
	case errors.Is(callerCtx.Err(), context.Canceled):
		*outcome = "canceled"
		return errorResult("the call was canceled")
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		*outcome = "timeout"
		return errorResult("the %s backend did not answer within %s; the query may be too heavy, "+
			"narrow it (shorter time range, more selective filters)", rt.backend.Prefix, a.cfg.CallTimeout.Std())
	}
	*outcome = kind
	if kind == "unavailable" {
		return errorResult("the %s backend is unavailable (%v); other backends are unaffected, retry later", rt.backend.Prefix, err)
	}
	return errorResult("the %s backend refused the call: %v", rt.backend.Prefix, err)
}

func errorResult(format string, args ...any) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(format, args...)}},
	}
}
