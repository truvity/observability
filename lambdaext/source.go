package lambdaext

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Defaults of the token timings; each is a Source field and an
// OTLP_LAMBDA_ setting.
const (
	// DefaultRefreshTimeout bounds one attempt of a refresh.
	DefaultRefreshTimeout = 5 * time.Second
	// DefaultRefreshMargin is how long before expiry a token is replaced.
	DefaultRefreshMargin = 6 * time.Minute
	// DefaultRefreshWait bounds the refresh the INVOKE event waits for.
	DefaultRefreshWait = 5 * time.Second
)

// Source hands out the access token for the OTLP endpoint, getting it on
// demand. A Lambda environment is frozen between invocations, so no timer
// can be trusted to have run; every caller checks the clock itself, and a
// refresh happens inside the call that needs it.
//
// The extension calls Ready on each INVOKE event, before it asks for the
// next event: a refresh that is due is finished there, or given up within
// RefreshWait, so none is left in flight when the environment freezes.
type Source struct {
	// Subject mints the STS identity token. Exchange trades it for the
	// access token and says how long that token lives.
	Subject  func(ctx context.Context) (string, error)
	Exchange func(ctx context.Context, subject string) (string, time.Duration, error)
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// Logf receives one line per failure window and one on recovery.
	Logf func(format string, args ...any)
	// OnToken sees each newly minted token (the optional token file).
	OnToken func(token string)
	// RefreshTimeout bounds one attempt of a refresh, the STS call and the
	// exchange together; zero is DefaultRefreshTimeout. Each attempt's
	// deadline is set when the attempt starts, and a failed attempt is tried
	// once more with a deadline of its own.
	RefreshTimeout time.Duration
	// RefreshMargin is how long before expiry a token is replaced; zero is
	// DefaultRefreshMargin. It is capped at half the token's life, so a
	// short-lived token is still reused.
	RefreshMargin time.Duration
	// RefreshWait bounds how long Ready waits for a refresh; zero is
	// DefaultRefreshWait.
	RefreshWait time.Duration
	// Backoff is how long after a failed refresh the next one waits; zero
	// is 5 seconds. Exporters retry fast, and each retry must not be an
	// STS call. Ready clears it.
	Backoff time.Duration

	mu        sync.Mutex
	token     string
	refreshAt time.Time
	expires   time.Time
	failedAt  time.Time
	lastErr   error
	failing   bool
	cur       *flight
}

// flight is one refresh; concurrent callers share it. Its fields other than
// done are guarded by Source.mu.
type flight struct {
	done   chan struct{}
	cancel context.CancelCauseFunc
	// deadline is the wall-clock deadline of the attempt running now.
	deadline time.Time
	err      error
	// finished is set once, by whoever closes done; abandoned says the
	// flight was dropped by Ready and its callers must start over.
	finished, abandoned bool
}

func (s *Source) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Source) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

func orDefault(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}

// refreshMargin is how long before expiry a token of the given life is
// replaced: the configured margin, at most half the life, at least a second.
func refreshMargin(lifetime, margin time.Duration) time.Duration {
	return max(min(orDefault(margin, DefaultRefreshMargin), lifetime/2), time.Second)
}

// ErrNoToken wraps the reason no token could be had.
var ErrNoToken = errors.New("lambdaext: no access token")

// Token returns a token that is valid now. A token inside its refresh
// window is replaced first; if the replacement fails and the old token has
// not expired, the old one is used.
func (s *Source) Token(ctx context.Context) (string, error) {
	backoff := orDefault(s.Backoff, 5*time.Second)
	for {
		s.mu.Lock()
		now := s.now()
		if s.token != "" && now.Before(s.refreshAt) {
			token := s.token
			s.mu.Unlock()
			return token, nil
		}
		usable := s.token != "" && now.Add(time.Second).Before(s.expires)
		if !s.failedAt.IsZero() && now.Sub(s.failedAt) < backoff {
			token, err := s.token, s.lastErr
			s.mu.Unlock()
			if usable {
				return token, nil
			}
			return "", err
		}
		f := s.cur
		if f == nil {
			f = s.startLocked()
		}
		s.mu.Unlock()

		select {
		case <-f.done:
		case <-ctx.Done():
			return "", ctx.Err()
		}

		if token, again, err := s.settled(f); !again {
			return token, err
		}
		// Dropped by Ready after a freeze: join the fresh one.
	}
}

// settled reads what a finished flight left: the new token, a still-valid
// old one, or the error. again is true when the flight was abandoned.
func (s *Source) settled(f *flight) (token string, again bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case f.abandoned:
		return "", true, nil
	case f.err == nil:
		return s.token, false, nil
	case s.token != "" && s.now().Add(time.Second).Before(s.expires):
		return s.token, false, nil
	}
	return "", false, f.err
}

// startLocked starts a refresh, detached from any caller's context. s.mu is
// held.
func (s *Source) startLocked() *flight {
	ctx, cancel := context.WithCancelCause(context.Background())
	f := &flight{done: make(chan struct{}), cancel: cancel}
	s.cur = f
	go s.run(ctx, f)
	return f
}

func (s *Source) run(ctx context.Context, f *flight) {
	token, ttl, err := s.fetch(ctx, f)
	if err != nil && ctx.Err() != nil {
		err = fmt.Errorf("%w: %w", ErrNoToken, context.Cause(ctx))
	}
	f.cancel(nil)

	s.mu.Lock()
	if f.finished { // abandoned: a fresh flight has taken over
		s.mu.Unlock()
		return
	}
	f.finished, f.err = true, err
	s.cur = nil
	if err != nil {
		s.failedAt, s.lastErr = s.now(), err
		first := !s.failing
		s.failing = true
		s.mu.Unlock()
		if first {
			s.logf("otlp-lambda: no access token, exports will be refused until one is obtained: %v", err)
		}
		close(f.done)
		return
	}
	now := s.now()
	s.token, s.expires = token, now.Add(ttl)
	s.refreshAt = now.Add(ttl - refreshMargin(ttl, s.RefreshMargin))
	s.failedAt, s.lastErr = time.Time{}, nil
	recovered := s.failing
	s.failing = false
	s.mu.Unlock()
	if recovered {
		s.logf("otlp-lambda: access token obtained again")
	}
	// The token file is written before any waiter is handed the token.
	if s.OnToken != nil {
		s.OnToken(token)
	}
	close(f.done)
}

// fetch tries twice. An issuer's refusal is an answer and is not retried;
// anything else (a timeout, a reset connection, a 5xx) is, with a new
// identity token, since the issuer may have consumed the first one.
func (s *Source) fetch(ctx context.Context, f *flight) (string, time.Duration, error) {
	var err error
	for attempt := range 2 {
		if attempt > 0 && (ctx.Err() != nil || errors.Is(err, errRefused)) {
			break
		}
		var token string
		var ttl time.Duration
		if token, ttl, err = s.attempt(ctx, f); err == nil {
			return token, ttl, nil
		}
	}
	return "", 0, err
}

// attempt is one try. Its deadline is made here, when it starts: a deadline
// made before a freeze has passed by the thaw, whatever the issuer does.
func (s *Source) attempt(ctx context.Context, f *flight) (string, time.Duration, error) {
	timeout := orDefault(s.RefreshTimeout, DefaultRefreshTimeout)
	s.mu.Lock()
	f.deadline = time.Now().Add(timeout)
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	subject, err := s.Subject(ctx)
	if err != nil {
		return "", 0, fmt.Errorf("%w: %w", ErrNoToken, err)
	}
	token, ttl, err := s.Exchange(ctx, subject)
	if err != nil {
		return "", 0, fmt.Errorf("%w: %w", ErrNoToken, err)
	}
	if token == "" || ttl <= 0 {
		return "", 0, fmt.Errorf("%w: the exchange returned no usable token", ErrNoToken)
	}
	return token, ttl, nil
}

// Invalidate drops the cached token if it is still the one given, because
// the upstream refused it. The next call mints a new one.
func (s *Source) Invalidate(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token == token {
		s.refreshAt = time.Time{}
		s.failedAt = time.Time{}
	}
}

// Warm starts a refresh if the token is inside its window, and waits for it
// as long as ctx allows.
func (s *Source) Warm(ctx context.Context) {
	_, _ = s.Token(ctx)
}

// errRefreshWait is the cause given to a refresh Ready gave up on.
var errRefreshWait = errors.New("the refresh did not finish before the environment could freeze")

// Ready is called on the INVOKE event, after a thaw and before the next
// /event/next. It forgets the backoff of a failure from before the freeze,
// drops a refresh whose attempt's deadline passed while frozen, and then
// finishes a refresh that is due, within RefreshWait. A refresh that does
// not finish in time is cancelled, so nothing is in flight when the
// environment freezes again; a still-valid token keeps being used, and the
// failure is logged once per failure window.
func (s *Source) Ready(ctx context.Context) {
	s.thaw()
	wait := orDefault(s.RefreshWait, DefaultRefreshWait)
	wctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	_, err := s.Token(wctx)
	if err == nil || wctx.Err() == nil || ctx.Err() != nil {
		return
	}
	s.mu.Lock()
	f := s.cur
	s.mu.Unlock()
	if f == nil {
		return
	}
	f.cancel(fmt.Errorf("%w (%v)", errRefreshWait, wait))
	// The attempt honours its context; wait a moment for it to unwind.
	select {
	case <-f.done:
	case <-time.After(250 * time.Millisecond):
	}
}

// thaw clears the backoff and abandons a refresh whose attempt can no longer
// succeed: its deadline passed, which after a freeze it has, while its
// goroutine has not yet run to see it.
func (s *Source) thaw() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failedAt, s.lastErr = time.Time{}, nil
	f := s.cur
	if f == nil || f.finished || f.deadline.IsZero() || time.Now().Before(f.deadline) {
		return
	}
	f.finished, f.abandoned = true, true
	s.cur = nil
	f.cancel(errors.New("abandoned after a freeze"))
	close(f.done)
}
