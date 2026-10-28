package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sort"
	"time"
)

// Sink receives the events that are new.
type Sink interface {
	Emit(ctx context.Context, events []Event) error
}

// Windower returns the events of a window (see Fetcher.Window).
type Windower interface {
	Window(ctx context.Context, start, end time.Time) ([]Event, error)
}

// Poller reads the log group by sliding windows.
//
// Each poll asks for [watermark-Overlap, now]: the overlap re-reads the last
// minutes so that an event CloudWatch delivered late (its timestamp is older
// than the moment it became queryable) is still found, and the persisted set
// of already-emitted @ptr values removes the repeats. The watermark moves to
// the window's end only after its events were handed to the sink and the
// state was written, so a crash repeats at most the one window in flight.
type Poller struct {
	Source    Windower
	Sink      Sink
	StatePath string
	State     *State
	Overlap   time.Duration
	MaxWindow time.Duration
	InitialLB time.Duration
	Metrics   *Metrics
	Logger    *slog.Logger
}

// Poll reads everything up to now.
func (p *Poller) Poll(ctx context.Context, now time.Time) error {
	end := now.UTC().Truncate(time.Second)

	var start time.Time
	if p.State.Watermark.IsZero() {
		start = end.Add(-p.InitialLB)
	} else {
		start = p.State.Watermark.Add(-p.Overlap)
	}

	for cs := start; cs.Before(end); {
		ce := cs.Add(p.MaxWindow)
		if ce.After(end) {
			ce = end
		}

		if err := p.chunk(ctx, cs, ce); err != nil {
			return err
		}

		cs = ce
	}

	return nil
}

func (p *Poller) chunk(ctx context.Context, start, end time.Time) error {
	events, err := p.Source.Window(ctx, start, end)
	if err != nil {
		p.Metrics.queries.WithLabelValues("error").Inc()

		return fmt.Errorf("window %s..%s: %w", start.Format(time.RFC3339), end.Format(time.RFC3339), err)
	}

	p.Metrics.queries.WithLabelValues("ok").Inc()

	fresh := make([]Event, 0, len(events))
	batch := map[string]bool{}

	for _, e := range events {
		k := dedupKey(e)
		if _, seen := p.State.Seen[k]; seen || batch[k] {
			continue
		}

		batch[k] = true

		fresh = append(fresh, e)
	}

	sort.SliceStable(fresh, func(i, j int) bool { return fresh[i].Timestamp.Before(fresh[j].Timestamp) })

	if len(fresh) > 0 {
		if err := p.Sink.Emit(ctx, fresh); err != nil {
			return fmt.Errorf("emit: %w", err)
		}
	}

	for _, e := range fresh {
		p.State.Seen[dedupKey(e)] = e.Timestamp.UnixMilli()
	}

	p.State.Watermark = end
	// The next window starts at end-Overlap; nothing earlier can come back.
	p.State.Prune(end.Add(-p.Overlap).Add(-time.Minute))

	if err := p.State.Save(p.StatePath); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	p.Metrics.events.Add(float64(len(fresh)))
	p.Metrics.watermark.Set(float64(end.Unix()))
	p.Metrics.lastSuccess.SetToCurrentTime()
	p.Logger.Info("window", "start", start, "end", end, "rows", len(events), "new", len(fresh))

	return nil
}

// dedupKey is the @ptr; a row without one (it should not happen) falls back to
// a digest of what identifies an event.
func dedupKey(e Event) string {
	if e.Ptr != "" {
		return e.Ptr
	}

	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s", e.Stream, e.Timestamp.UnixMilli(), e.Message)))

	return "h:" + hex.EncodeToString(sum[:])
}
