package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/smithy-go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

type apiErr struct{ code string }

func (e apiErr) Error() string                 { return e.code }
func (e apiErr) ErrorCode() string             { return e.code }
func (e apiErr) ErrorMessage() string          { return e.code }
func (e apiErr) ErrorFault() smithy.ErrorFault { return smithy.FaultServer }

// fakeAPI is a log group: StartQuery records the window, GetQueryResults
// answers Running once and then the stored events inside the window, cut at
// the query's limit, oldest first.
type fakeAPI struct {
	mu       sync.Mutex
	events   []Event // the group's content
	queries  map[string][2]int64
	limits   map[string]int
	nextID   int
	starts   int
	windows  [][2]int64
	throttle int // StartQuery calls to refuse with LimitExceeded
	polled   map[string]bool
	stopped  int
}

func newFake(events ...Event) *fakeAPI {
	return &fakeAPI{events: events, queries: map[string][2]int64{}, limits: map[string]int{}, polled: map[string]bool{}}
}

func (f *fakeAPI) StartQuery(_ context.Context, in *cloudwatchlogs.StartQueryInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.StartQueryOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.starts++

	if f.throttle > 0 {
		f.throttle--

		return nil, apiErr{"LimitExceededException"}
	}

	f.nextID++
	id := fmt.Sprintf("q%d", f.nextID)
	f.queries[id] = [2]int64{*in.StartTime, *in.EndTime}
	f.limits[id] = int(*in.Limit)
	f.windows = append(f.windows, [2]int64{*in.StartTime, *in.EndTime})

	return &cloudwatchlogs.StartQueryOutput{QueryId: &id}, nil
}

func (f *fakeAPI) GetQueryResults(_ context.Context, in *cloudwatchlogs.GetQueryResultsInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetQueryResultsOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	id := *in.QueryId
	if !f.polled[id] {
		f.polled[id] = true

		return &cloudwatchlogs.GetQueryResultsOutput{Status: types.QueryStatusRunning}, nil
	}

	w := f.queries[id]

	var hit []Event

	for _, e := range f.events {
		if e.Timestamp.Unix() >= w[0] && e.Timestamp.Unix() <= w[1] {
			hit = append(hit, e)
		}
	}

	sort.SliceStable(hit, func(i, j int) bool { return hit[i].Timestamp.Before(hit[j].Timestamp) })

	if len(hit) > f.limits[id] {
		hit = hit[:f.limits[id]]
	}

	var rows [][]types.ResultField

	for _, e := range hit {
		rows = append(rows, []types.ResultField{
			{Field: strPtr("@timestamp"), Value: strPtr(e.Timestamp.Format(insightsTime))},
			{Field: strPtr("@message"), Value: strPtr(e.Message)},
			{Field: strPtr("@logStream"), Value: strPtr(e.Stream)},
			{Field: strPtr("@ptr"), Value: strPtr(e.Ptr)},
		})
	}

	return &cloudwatchlogs.GetQueryResultsOutput{Status: types.QueryStatusComplete, Results: rows}, nil
}

func (f *fakeAPI) StopQuery(context.Context, *cloudwatchlogs.StopQueryInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.StopQueryOutput, error) {
	f.stopped++

	return &cloudwatchlogs.StopQueryOutput{}, nil
}

type captureSink struct{ got []Event }

func (c *captureSink) Emit(_ context.Context, e []Event) error {
	c.got = append(c.got, e...)

	return nil
}

func ev(n int, at time.Time) Event {
	return Event{Ptr: fmt.Sprintf("ptr-%d", n), Stream: "authenticator-abc", Message: fmt.Sprintf("m%d", n), Timestamp: at}
}

func fetcher(api API, m *Metrics, cap int) *Fetcher {
	return &Fetcher{
		API: api, LogGroup: "/aws/eks/c/cluster", Streams: []string{"kube-apiserver-audit-", "authenticator-"},
		Metrics: m, Cap: cap, Retries: 5, Backoff: time.Millisecond,
		Sleep: func(context.Context, time.Duration) error { return nil },
	}
}

func poller(t *testing.T, src Windower, sink Sink, m *Metrics, state *State, path string) *Poller {
	t.Helper()

	return &Poller{
		Source: src, Sink: sink, StatePath: path, State: state,
		Overlap: 3 * time.Minute, MaxWindow: 15 * time.Minute, InitialLB: 10 * time.Minute,
		Metrics: m, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func metrics() *Metrics { return NewMetrics(prometheus.NewRegistry()) }

func TestQueryHasStreamFilterAndPrefilter(t *testing.T) {
	f := fetcher(newFake(), metrics(), 0)
	f.Filter = `@message like /access denied/`

	q := f.Query()
	assert.Contains(t, q, "| filter @logStream like /^(kube-apiserver-audit-|authenticator-)/")
	assert.Contains(t, q, "| filter @message like /access denied/")
	assert.Contains(t, q, "| sort @timestamp asc")
	assert.True(t, strings.HasSuffix(q, "| limit 10000"))
}

func TestWindowsOverlapAndDedup(t *testing.T) {
	// e1 and e2 are old; e3 arrives late: its timestamp is before the first
	// poll's end but CloudWatch makes it queryable only after that poll.
	e1 := ev(1, t0.Add(-5*time.Minute))
	e2 := ev(2, t0.Add(-1*time.Minute))
	e3 := ev(3, t0.Add(-30*time.Second))

	api := newFake(e1, e2)
	sink := &captureSink{}
	m := metrics()
	dir := t.TempDir()
	p := poller(t, fetcher(api, m, 0), sink, m, NewState(), filepath.Join(dir, "state.json"))

	require.NoError(t, p.Poll(context.Background(), t0))
	assert.Len(t, sink.got, 2)

	// Second poll a minute later: the window starts 3 minutes before the
	// watermark, re-reads e1 and e2 (no repeat emitted) and finds the late e3.
	api.events = append(api.events, e3)

	require.NoError(t, p.Poll(context.Background(), t0.Add(time.Minute)))

	var ptrs []string
	for _, e := range sink.got {
		ptrs = append(ptrs, e.Ptr)
	}

	assert.Equal(t, []string{"ptr-1", "ptr-2", "ptr-3"}, ptrs)
	assert.Equal(t, t0.Add(-3*time.Minute).Unix(), api.windows[len(api.windows)-1][0])
	assert.Equal(t, t0.Add(time.Minute).Unix(), api.windows[len(api.windows)-1][1])
}

func TestDedupSurvivesRestart(t *testing.T) {
	e1 := ev(1, t0.Add(-1*time.Minute))
	api := newFake(e1)
	path := filepath.Join(t.TempDir(), "state.json")
	m := metrics()

	sink1 := &captureSink{}
	require.NoError(t, poller(t, fetcher(api, m, 0), sink1, m, NewState(), path).Poll(context.Background(), t0))
	require.Len(t, sink1.got, 1)

	// A new process: the state comes from the file.
	state, err := LoadState(path)
	require.NoError(t, err)
	assert.Equal(t, t0, state.Watermark)

	e2 := ev(2, t0.Add(30*time.Second))
	api.events = append(api.events, e2)

	sink2 := &captureSink{}
	require.NoError(t, poller(t, fetcher(api, m, 0), sink2, m, state, path).Poll(context.Background(), t0.Add(time.Minute)))

	// e1 is inside the overlap and was re-read, but not emitted again; e2 is new.
	require.Len(t, sink2.got, 1)
	assert.Equal(t, "ptr-2", sink2.got[0].Ptr)
}

func TestRestartAfterLongDowntimeReadsInChunks(t *testing.T) {
	api := newFake(ev(1, t0.Add(-100*time.Minute)), ev(2, t0.Add(-50*time.Minute)), ev(3, t0.Add(-1*time.Minute)))
	m := metrics()
	sink := &captureSink{}
	state := NewState()
	state.Watermark = t0.Add(-2 * time.Hour)

	p := poller(t, fetcher(api, m, 0), sink, m, state, filepath.Join(t.TempDir(), "s.json"))
	require.NoError(t, p.Poll(context.Background(), t0))

	assert.Len(t, sink.got, 3)

	for _, w := range api.windows {
		assert.LessOrEqual(t, w[1]-w[0], int64(15*60))
	}
}

func TestRowCapSplitsTheWindow(t *testing.T) {
	// 12 events one per 10 s inside a 2-minute window, cap 5: the window must
	// be split until every query is under the cap, and every event comes back once.
	var all []Event
	for i := range 12 {
		all = append(all, ev(i, t0.Add(-2*time.Minute+time.Duration(i)*10*time.Second)))
	}

	api := newFake(all...)
	m := metrics()
	f := fetcher(api, m, 5)

	got, err := f.Window(context.Background(), t0.Add(-2*time.Minute), t0)
	require.NoError(t, err)

	seen := map[string]bool{}
	for _, e := range got {
		seen[e.Ptr] = true
	}

	assert.Len(t, seen, 12)
	assert.Greater(t, api.starts, 1)

	// Through the poller the repeats across halves are removed.
	sink := &captureSink{}
	p := poller(t, f, sink, m, NewState(), filepath.Join(t.TempDir(), "s.json"))
	p.InitialLB = 2 * time.Minute
	require.NoError(t, p.Poll(context.Background(), t0))
	assert.Len(t, sink.got, 12)
}

func TestOneSecondAtTheCapIsCountedNotSplit(t *testing.T) {
	var all []Event
	for i := range 6 {
		all = append(all, ev(i, t0))
	}

	api := newFake(all...)
	m := metrics()
	got, err := fetcher(api, m, 5).Window(context.Background(), t0, t0.Add(time.Second))
	require.NoError(t, err)
	assert.NotEmpty(t, got)
	assert.Less(t, api.starts, 20)
}

func TestThrottlingBacksOffThenSucceeds(t *testing.T) {
	api := newFake(ev(1, t0.Add(-time.Minute)))
	api.throttle = 3

	var slept []time.Duration

	m := metrics()
	f := fetcher(api, m, 0)
	f.Sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }

	got, err := f.Window(context.Background(), t0.Add(-2*time.Minute), t0)
	require.NoError(t, err)
	assert.Len(t, got, 1)
	assert.Equal(t, 4, api.starts)
	// three backoffs plus the one wait for the running query, growing
	require.GreaterOrEqual(t, len(slept), 3)
	assert.GreaterOrEqual(t, slept[1], 2*time.Millisecond)
	assert.GreaterOrEqual(t, slept[2], 4*time.Millisecond)
}

func TestThrottlingGivesUpAndLeavesStateAlone(t *testing.T) {
	api := newFake()
	api.throttle = 1000
	m := metrics()
	state := NewState()
	state.Watermark = t0.Add(-time.Minute)

	p := poller(t, fetcher(api, m, 0), &captureSink{}, m, state, filepath.Join(t.TempDir(), "s.json"))
	require.Error(t, p.Poll(context.Background(), t0))
	assert.Equal(t, t0.Add(-time.Minute), state.Watermark)
	assert.Equal(t, 5, api.starts)
}

func TestSinkFailureDoesNotAdvance(t *testing.T) {
	api := newFake(ev(1, t0.Add(-time.Minute)))
	m := metrics()
	state := NewState()
	path := filepath.Join(t.TempDir(), "s.json")

	p := poller(t, fetcher(api, m, 0), failSink{}, m, state, path)
	require.Error(t, p.Poll(context.Background(), t0))
	assert.True(t, state.Watermark.IsZero())
	assert.Empty(t, state.Seen)
}

type failSink struct{}

func (failSink) Emit(context.Context, []Event) error { return fmt.Errorf("collector down") }

func TestStatePrunesWhatCannotReturn(t *testing.T) {
	s := NewState()
	s.Seen["old"] = t0.Add(-time.Hour).UnixMilli()
	s.Seen["new"] = t0.UnixMilli()
	s.Prune(t0.Add(-time.Minute))
	assert.Contains(t, s.Seen, "new")
	assert.NotContains(t, s.Seen, "old")
}

func TestOTLPRecordShape(t *testing.T) {
	sink := &OTLPSink{Region: "eu-x-1", LogGroup: "/aws/eks/c/cluster"}
	req := sink.Request([]Event{
		{Ptr: "1", Stream: "authenticator-a", Message: "hello", Timestamp: t0},
		{Ptr: "2", Stream: "kube-apiserver-audit-b", Message: "{}", Timestamp: t0},
		{Ptr: "3", Stream: "authenticator-a", Message: "again", Timestamp: t0},
	}, t0)

	require.Len(t, req.ResourceLogs, 2)

	rl := req.ResourceLogs[0]
	attrs := map[string]string{}

	for _, kv := range rl.Resource.Attributes {
		attrs[kv.Key] = kv.Value.GetStringValue()
	}

	assert.Equal(t, map[string]string{"aws.region": "eu-x-1", "cloudwatch.log.group.name": "/aws/eks/c/cluster", "cloudwatch.log.stream": "authenticator-a"}, attrs)
	require.Len(t, rl.ScopeLogs[0].LogRecords, 2)
	assert.Equal(t, "hello", rl.ScopeLogs[0].LogRecords[0].Body.GetStringValue())
	assert.Equal(t, uint64(t0.UnixNano()), rl.ScopeLogs[0].LogRecords[0].TimeUnixNano)
}

func TestConfigRefusals(t *testing.T) {
	c := &Config{Region: "r", LogGroup: "x", PollInterval: time.Minute, MaxWindow: time.Minute, InitialLB: time.Minute}
	err := c.Check()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--log-group")
	assert.Contains(t, err.Error(), "--stream-prefix")
}
