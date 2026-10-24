package lambdaext_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truvity/observability/lambdaext"
)

// A Lambda freeze stops every goroutine while the wall clock runs on. A test
// cannot stop the scheduler, so a "frozen" call here is one that ignores its
// context until released: its deadline passes and it does not notice, as a
// frozen one would not.

func (h *harness) logged() []string {
	h.logMu.Lock()
	defer h.logMu.Unlock()
	return append([]string(nil), h.logs...)
}

func TestRefreshMarginIsSixMinutesBeforeExpiry(t *testing.T) {
	for _, tc := range []struct {
		name          string
		ttl, margin   time.Duration
		cached, fresh time.Duration // a call at cached reuses, at fresh refreshes
	}{
		{"default", 15 * time.Minute, 0, 8*time.Minute + 59*time.Second, 9*time.Minute + time.Second},
		{"configured", 15 * time.Minute, 2 * time.Minute, 12*time.Minute + 59*time.Second, 13*time.Minute + time.Second},
		{"capped at half a short life", 6 * time.Second, 0, 2900 * time.Millisecond, 3100 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(tc.ttl)
			h.src.RefreshMargin = tc.margin
			first, _ := h.src.Token(context.Background())
			h.clk.advance(tc.cached)
			if got, _ := h.src.Token(context.Background()); got != first || h.exchanges.Load() != 1 {
				t.Fatalf("at %v: want the cached token, got %q after %d exchanges", tc.cached, got, h.exchanges.Load())
			}
			h.clk.advance(tc.fresh - tc.cached)
			if got, _ := h.src.Token(context.Background()); got == first || h.exchanges.Load() != 2 {
				t.Fatalf("at %v: want a refresh, got %q after %d exchanges", tc.fresh, got, h.exchanges.Load())
			}
		})
	}
}

func TestAFailedAttemptIsRetriedOnceWithItsOwnDeadline(t *testing.T) {
	h := newHarness(15 * time.Minute)
	h.src.RefreshTimeout = time.Second
	var mu sync.Mutex
	var deadlines []time.Time
	var failedAt time.Time
	h.src.Exchange = func(ctx context.Context, subject string) (string, time.Duration, error) {
		d, _ := ctx.Deadline()
		mu.Lock()
		defer mu.Unlock()
		deadlines = append(deadlines, d)
		if len(deadlines) == 1 {
			time.Sleep(20 * time.Millisecond)
			failedAt = time.Now()
			return "", 0, errors.New("connection reset by peer")
		}
		return "tok(" + subject + ")", 15 * time.Minute, nil
	}
	got, err := h.src.Token(context.Background())
	if err != nil || got != "tok(subject-2)" {
		t.Fatalf("want the retry's token minted from a new identity token, got %q %v", got, err)
	}
	if len(deadlines) != 2 || !deadlines[1].After(failedAt) {
		t.Fatalf("the retry needs a deadline set when it starts: %v, first failed at %v", deadlines, failedAt)
	}
	if logs := h.logged(); len(logs) != 0 {
		t.Fatalf("a refresh saved by its retry is not a failure: %v", logs)
	}
}

func TestARefusalIsNotRetriedButAnIssuer5xxIs(t *testing.T) {
	issuer := newFakeIssuer(t, 900)
	cfg := lambdaext.Config{Issuer: issuer.URL, OTLPAudience: "otlp"}
	src := &lambdaext.Source{
		Subject:  func(context.Context) (string, error) { return "sts-jwt", nil },
		Exchange: lambdaext.ExchangeFunc(cfg, nil),
	}
	issuer.mu.Lock()
	issuer.fail5xx = 1
	issuer.mu.Unlock()
	if tok, err := src.Token(context.Background()); err != nil || tok != "access-2" || issuer.count() != 2 {
		t.Fatalf("a 502 is retried once: %q %v after %d exchanges", tok, err, issuer.count())
	}

	issuer2 := newFakeIssuer(t, 900)
	issuer2.refuse = true
	src2 := &lambdaext.Source{
		Subject:  func(context.Context) (string, error) { return "sts-jwt", nil },
		Exchange: lambdaext.ExchangeFunc(lambdaext.Config{Issuer: issuer2.URL, OTLPAudience: "otlp"}, nil),
	}
	if _, err := src2.Token(context.Background()); err == nil || issuer2.count() != 1 {
		t.Fatalf("a refusal is an answer, not retried: %v after %d exchanges", err, issuer2.count())
	}
}

func TestARefreshStartedBeforeAFreezeDoesNotKeepItsStaleDeadline(t *testing.T) {
	h := newHarness(15 * time.Minute)
	h.src.RefreshTimeout = 100 * time.Millisecond
	frozen := make(chan struct{})
	entered := make(chan struct{})
	var calls atomic.Int32
	var mu sync.Mutex
	var deadlines []time.Time
	h.src.Exchange = func(ctx context.Context, _ string) (string, time.Duration, error) {
		d, _ := ctx.Deadline()
		mu.Lock()
		deadlines = append(deadlines, d)
		mu.Unlock()
		if calls.Add(1) == 1 {
			close(entered)
			<-frozen // frozen: blind to its own deadline
			return "pre-freeze", 15 * time.Minute, nil
		}
		return "post-thaw", 15 * time.Minute, nil
	}
	// An export just before the function returned starts the refresh.
	preFreeze := make(chan string, 1)
	go func() { tok, _ := h.src.Token(context.Background()); preFreeze <- tok }()
	<-entered
	time.Sleep(200 * time.Millisecond) // the freeze outlasts the deadline
	h.clk.advance(5 * time.Minute)

	thawAt := time.Now()
	h.src.Ready(context.Background()) // INVOKE after the thaw
	if took := time.Since(thawAt); took > time.Second {
		t.Fatalf("Ready waited %v on a refresh that could no longer succeed", took)
	}
	mu.Lock()
	got := append([]time.Time(nil), deadlines...)
	mu.Unlock()
	if len(got) != 2 || !got[1].After(thawAt) {
		t.Fatalf("the refresh after the thaw needs a deadline made after the thaw (%v): %v", thawAt, got)
	}
	select {
	case tok := <-preFreeze:
		if tok != "post-thaw" {
			t.Fatalf("the caller waiting since before the freeze got %q", tok)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the caller waiting since before the freeze was left hanging")
	}
	close(frozen) // the stale attempt returns late; its answer is not used
	time.Sleep(50 * time.Millisecond)
	if tok, _ := h.src.Token(context.Background()); tok != "post-thaw" {
		t.Fatalf("the abandoned refresh replaced the token: %q", tok)
	}
	for _, line := range h.logged() {
		if strings.Contains(line, "no access token") {
			t.Fatalf("a refresh restarted after a freeze is not a failure: %v", h.logged())
		}
	}
}

func TestReadyKeepsARefreshThatIsStillInTime(t *testing.T) {
	// INVOKE may be read just after an export started the refresh: that one
	// is fresh and is joined, not restarted.
	h := newHarness(15 * time.Minute)
	gate := make(chan struct{})
	entered := make(chan struct{})
	var calls atomic.Int32
	h.src.Exchange = func(ctx context.Context, _ string) (string, time.Duration, error) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		select {
		case <-gate:
		case <-ctx.Done():
			return "", 0, ctx.Err()
		}
		return "tok", 15 * time.Minute, nil
	}
	go func() { _, _ = h.src.Token(context.Background()) }()
	<-entered
	go func() { time.Sleep(50 * time.Millisecond); close(gate) }()
	h.src.Ready(context.Background())
	if tok, _ := h.src.Token(context.Background()); tok != "tok" || calls.Load() != 1 {
		t.Fatalf("want the shared refresh's token after one exchange, got %q after %d", tok, calls.Load())
	}
}

func TestReadyFinishesADueRefreshAndSkipsAFreshToken(t *testing.T) {
	h := newHarness(15 * time.Minute)
	inner := h.src.Exchange
	h.src.Exchange = func(ctx context.Context, subject string) (string, time.Duration, error) {
		time.Sleep(100 * time.Millisecond)
		return inner(ctx, subject)
	}
	h.src.Ready(context.Background()) // first INVOKE: no token yet
	if h.exchanges.Load() != 1 {
		t.Fatalf("Ready returned before the refresh finished: %d exchanges", h.exchanges.Load())
	}
	start := time.Now()
	h.src.Ready(context.Background()) // token fresh: nothing to do
	if h.exchanges.Load() != 1 || time.Since(start) > 50*time.Millisecond {
		t.Fatalf("a fresh token needs no refresh: %d exchanges in %v", h.exchanges.Load(), time.Since(start))
	}
	h.clk.advance(10 * time.Minute) // inside the window
	h.src.Ready(context.Background())
	if h.exchanges.Load() != 2 {
		t.Fatalf("a due refresh is done in Ready: %d exchanges", h.exchanges.Load())
	}
}

func TestReadyIsBoundedCancelsTheRefreshAndKeepsTheOldToken(t *testing.T) {
	h := newHarness(15 * time.Minute)
	first, _ := h.src.Token(context.Background())
	h.clk.advance(10 * time.Minute) // due, and valid for 5 more

	h.src.RefreshWait = 150 * time.Millisecond
	cause := make(chan error, 1)
	h.src.Exchange = func(ctx context.Context, _ string) (string, time.Duration, error) {
		<-ctx.Done()
		cause <- context.Cause(ctx)
		return "", 0, ctx.Err()
	}
	start := time.Now()
	h.src.Ready(context.Background())
	if took := time.Since(start); took > time.Second {
		t.Fatalf("Ready is bounded by RefreshWait, took %v", took)
	}
	select {
	case err := <-cause:
		if err == nil || !strings.Contains(err.Error(), "did not finish") {
			t.Fatalf("the refresh was cancelled for the wrong reason: %v", err)
		}
	default:
		t.Fatal("the refresh is still in flight after Ready returned")
	}
	if tok, err := h.src.Token(context.Background()); err != nil || tok != first {
		t.Fatalf("the still-valid token keeps being used: %q %v", tok, err)
	}
	logs := h.logged()
	if len(logs) != 1 || !strings.Contains(logs[0], "did not finish") {
		t.Fatalf("one line names the bound: %v", logs)
	}

	// The next INVOKE tries again at once, the backoff notwithstanding.
	var n atomic.Int32
	h.src.Exchange = func(context.Context, string) (string, time.Duration, error) {
		return fmt.Sprintf("next-%d", n.Add(1)), 15 * time.Minute, nil
	}
	h.src.Ready(context.Background())
	if tok, _ := h.src.Token(context.Background()); tok != "next-1" {
		t.Fatalf("the next INVOKE refreshes: %q", tok)
	}
	if logs = h.logged(); len(logs) != 2 || !strings.Contains(logs[1], "again") {
		t.Fatalf("recovery is logged once: %v", logs)
	}
}

func TestReadyClearsTheBackoffOfAFailureBeforeTheFreeze(t *testing.T) {
	h := newHarness(15 * time.Minute)
	h.failNext.Store(true)
	if _, err := h.src.Token(context.Background()); err == nil {
		t.Fatal("want a failure")
	}
	before := h.exchanges.Load()
	_, _ = h.src.Token(context.Background()) // inside the backoff
	if h.exchanges.Load() != before {
		t.Fatal("the backoff must hold between INVOKEs")
	}
	h.failNext.Store(false)
	h.src.Ready(context.Background()) // INVOKE, within 5s of the failure
	if tok, err := h.src.Token(context.Background()); err != nil || tok == "" {
		t.Fatalf("Ready retries despite the backoff: %q %v", tok, err)
	}
}
