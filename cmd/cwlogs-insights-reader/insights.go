package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/smithy-go"
)

// RowCap is the most rows one Logs Insights query returns (the API's own
// ceiling for `limit`). A query that comes back with this many rows may have
// been cut off, and its window is split.
const RowCap = 10000

// API is the slice of the CloudWatch Logs client the reader uses; the tests
// fake it.
type API interface {
	StartQuery(ctx context.Context, in *cloudwatchlogs.StartQueryInput,
		opts ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.StartQueryOutput, error)
	GetQueryResults(ctx context.Context, in *cloudwatchlogs.GetQueryResultsInput,
		opts ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetQueryResultsOutput, error)
	StopQuery(ctx context.Context, in *cloudwatchlogs.StopQueryInput, opts ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.StopQueryOutput, error)
}

// Event is one log event as Logs Insights returns it.
type Event struct {
	// Ptr is the stable identifier of the event inside the log group (@ptr);
	// it is what makes deduplication exact.
	Ptr       string
	Stream    string
	Message   string
	Timestamp time.Time
}

// Fetcher runs Logs Insights queries over a window.
type Fetcher struct {
	API       API
	LogGroup  string
	Streams   []string // stream name prefixes
	Filter    string   // optional coarse prefilter, a Logs Insights filter expression
	Metrics   *Metrics
	Cap       int           // row cap per query; zero means RowCap
	PollEvery time.Duration // between GetQueryResults calls
	Retries   int           // attempts at a throttled call before giving up
	Backoff   time.Duration // first backoff; doubles, capped at 30 s, jittered
	Sleep     func(ctx context.Context, d time.Duration) error
}

func (f *Fetcher) cap() int {
	if f.Cap > 0 {
		return f.Cap
	}

	return RowCap
}

// Query is the Logs Insights query for one window. @ptr is not listed in
// `fields`: Insights returns it with every row.
func (f *Fetcher) Query() string {
	alts := make([]string, 0, len(f.Streams))
	for _, s := range f.Streams {
		alts = append(alts, regexQuote(s))
	}

	var b strings.Builder

	b.WriteString("fields @timestamp, @message, @logStream\n")
	fmt.Fprintf(&b, "| filter @logStream like /^(%s)/\n", strings.Join(alts, "|"))

	if f.Filter != "" {
		fmt.Fprintf(&b, "| filter %s\n", f.Filter)
	}

	b.WriteString("| sort @timestamp asc\n")
	fmt.Fprintf(&b, "| limit %d", f.cap())

	return b.String()
}

// regexQuote escapes a literal for a Logs Insights /regex/ literal.
func regexQuote(s string) string {
	var b strings.Builder

	for _, r := range s {
		if strings.ContainsRune(`\.+*?()|[]{}^$/`, r) {
			b.WriteByte('\\')
		}

		b.WriteRune(r)
	}

	return b.String()
}

// Window returns the events of [start, end] (whole seconds, both inclusive),
// splitting the window in two whenever a query comes back with RowCap rows.
// Rows may repeat across the two halves; the caller dedups by Ptr.
func (f *Fetcher) Window(ctx context.Context, start, end time.Time) ([]Event, error) {
	events, err := f.run(ctx, start, end)
	if err != nil {
		return nil, err
	}

	if len(events) < f.cap() {
		return events, nil
	}

	span := end.Sub(start)
	if span <= time.Second {
		// One second holding RowCap matches cannot be split further.
		f.Metrics.truncated.Inc()

		return events, nil
	}

	f.Metrics.splits.Inc()

	mid := start.Add(span / 2).Truncate(time.Second)
	if !mid.After(start) {
		mid = start.Add(time.Second)
	}

	left, err := f.Window(ctx, start, mid)
	if err != nil {
		return nil, err
	}

	right, err := f.Window(ctx, mid, end)
	if err != nil {
		return nil, err
	}

	return append(left, right...), nil
}

func (f *Fetcher) run(ctx context.Context, start, end time.Time) ([]Event, error) {
	var out *cloudwatchlogs.StartQueryOutput

	err := f.retry(ctx, func() error {
		var err error

		out, err = f.API.StartQuery(ctx, &cloudwatchlogs.StartQueryInput{
			LogGroupName: &f.LogGroup,
			StartTime:    int64Ptr(start.Unix()),
			EndTime:      int64Ptr(end.Unix()),
			QueryString:  strPtr(f.Query()),
			Limit:        int32Ptr(int32(f.cap())), //nolint:gosec
		})

		return err
	})
	if err != nil {
		return nil, fmt.Errorf("start query: %w", err)
	}

	id := out.QueryId

	for {
		var res *cloudwatchlogs.GetQueryResultsOutput

		err := f.retry(ctx, func() error {
			var err error

			res, err = f.API.GetQueryResults(ctx, &cloudwatchlogs.GetQueryResultsInput{QueryId: id})

			return err
		})
		if err != nil {
			f.stop(id)

			return nil, fmt.Errorf("get query results: %w", err)
		}

		switch res.Status {
		case types.QueryStatusComplete:
			return parseRows(res.Results)
		case types.QueryStatusFailed, types.QueryStatusCancelled, types.QueryStatusTimeout:
			return nil, fmt.Errorf("query ended %s", res.Status)
		default: // Scheduled, Running, Unknown: wait
		}

		if err := f.Sleep(ctx, f.PollEvery); err != nil {
			f.stop(id)

			return nil, err
		}
	}
}

// stop cancels a query the reader no longer waits for, so it does not count
// against the account's concurrent-query limit until it finishes by itself.
func (f *Fetcher) stop(id *string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, _ = f.API.StopQuery(ctx, &cloudwatchlogs.StopQueryInput{QueryId: id})
}

// retry runs call, backing off while the API throttles or the concurrent
// query limit is hit.
func (f *Fetcher) retry(ctx context.Context, call func() error) error {
	delay := f.Backoff

	var err error

	for attempt := 0; attempt < f.Retries; attempt++ {
		if err = call(); err == nil || !retryable(err) {
			return err
		}

		f.Metrics.throttled.Inc()

		jitter := time.Duration(rand.Int64N(int64(delay)/2 + 1))
		if serr := f.Sleep(ctx, delay+jitter); serr != nil {
			return serr
		}

		delay = min(delay*2, 30*time.Second)
	}

	return fmt.Errorf("still throttled after %d attempts: %w", f.Retries, err)
}

func retryable(err error) bool {
	var api smithy.APIError
	if !errors.As(err, &api) {
		return false
	}

	switch api.ErrorCode() {
	case "ThrottlingException", "Throttling", "LimitExceededException", "ServiceUnavailableException", "RequestLimitExceeded", "TooManyRequestsException":
		return true
	}

	return false
}

const insightsTime = "2006-01-02 15:04:05.000"

func parseRows(rows [][]types.ResultField) ([]Event, error) {
	events := make([]Event, 0, len(rows))

	for _, row := range rows {
		var e Event

		var ts string

		for _, field := range row {
			if field.Field == nil || field.Value == nil {
				continue
			}

			switch *field.Field {
			case "@ptr":
				e.Ptr = *field.Value
			case "@logStream":
				e.Stream = *field.Value
			case "@message":
				e.Message = *field.Value
			case "@timestamp":
				ts = *field.Value
			}
		}

		t, err := time.ParseInLocation(insightsTime, ts, time.UTC)
		if err != nil {
			return nil, fmt.Errorf("row timestamp %q: %w", ts, err)
		}

		e.Timestamp = t

		events = append(events, e)
	}

	return events, nil
}

func int64Ptr(v int64) *int64 { return &v }
func int32Ptr(v int32) *int32 { return &v }
func strPtr(v string) *string { return &v }
