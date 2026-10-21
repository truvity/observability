package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeResolver struct {
	mu    sync.Mutex
	addrs []string
	err   error
	calls int
}

func (f *fakeResolver) LookupHost(_ context.Context, _ string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls++

	return f.addrs, f.err
}

// replica is one fake Alertmanager. clientFor gives each its own fake IP and
// dials the real listener behind it, so the test needs no extra loopback
// addresses.
type replica struct {
	server *httptest.Server
	hits   int
	status int
	host   string
	mu     sync.Mutex
}

func newReplica(t *testing.T, status int) *replica {
	t.Helper()

	r := &replica{status: status}
	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.hits++
		r.host = req.Host
		r.mu.Unlock()
		w.WriteHeader(r.status)
	}))
	t.Cleanup(r.server.Close)

	return r
}

func (r *replica) addr() string { return r.server.Listener.Addr().String() }

// clientFor builds a client whose resolver answers one fake IP per replica.
func clientFor(replicas ...*replica) (*AlertmanagerClient, *fakeResolver, prometheus.Counter) {
	var addrs []string

	byIP := map[string]*replica{}

	for i, r := range replicas {
		ip := net.IPv4(10, 0, 0, byte(i+1)).String()
		addrs = append(addrs, ip)
		byIP[ip] = r
	}

	res := &fakeResolver{addrs: addrs}
	failures := prometheus.NewCounter(prometheus.CounterOpts{Name: "f"})

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			ip, _, _ := net.SplitHostPort(address)

			r, ok := byIP[ip]
			if !ok {
				return nil, errors.New("unexpected dial to " + address)
			}

			return (&net.Dialer{}).DialContext(ctx, network, r.addr())
		},
	}

	return &AlertmanagerClient{
		URL:        "http://alertmanager.example:9093",
		HTTPClient: &http.Client{Transport: transport, Timeout: 2 * time.Second},
		Resolver:   res,
		Failures:   failures,
	}, res, failures
}

func testAlert() AlertmanagerAlert {
	now := time.Now().UTC()

	return AlertmanagerAlert{Labels: map[string]string{"alertname": "X"}, StartsAt: now, EndsAt: now.Add(time.Hour)}
}

func TestPostFansOutToEveryReplica(t *testing.T) {
	a, b, c := newReplica(t, 200), newReplica(t, 200), newReplica(t, 200)
	client, _, failures := clientFor(a, b, c)

	require.NoError(t, client.Post(testAlert()))

	for _, r := range []*replica{a, b, c} {
		assert.Equal(t, 1, r.hits)
		assert.Equal(t, "alertmanager.example:9093", r.host, "Host header keeps the configured name")
	}

	assert.InDelta(t, 0, plainCounter(failures), 0)
}

func TestPostOneReplicaFailingStillDelivers(t *testing.T) {
	a, b, c := newReplica(t, 200), newReplica(t, 503), newReplica(t, 200)
	client, _, failures := clientFor(a, b, c)

	require.NoError(t, client.Post(testAlert()))

	assert.Equal(t, 1, a.hits)
	assert.Equal(t, 1, b.hits)
	assert.Equal(t, 1, c.hits)
	assert.InDelta(t, 1, plainCounter(failures), 0)
}

func TestPostAllReplicasFailing(t *testing.T) {
	a, b := newReplica(t, 500), newReplica(t, 503)
	client, _, failures := clientFor(a, b)

	require.Error(t, client.Post(testAlert()))
	assert.InDelta(t, 2, plainCounter(failures), 0)
}

func TestPostSingleAddressIsUnchanged(t *testing.T) {
	a := newReplica(t, 200)
	u, err := url.Parse(a.server.URL)
	require.NoError(t, err)

	res := &fakeResolver{addrs: []string{"192.0.2.1"}}
	client := &AlertmanagerClient{URL: "http://localhost:" + u.Port(), HTTPClient: a.server.Client(), Resolver: res}

	require.NoError(t, client.Post(testAlert()))
	assert.Equal(t, 1, a.hits)
	assert.Equal(t, "localhost:"+u.Port(), a.host, "one record: the URL is used as written")
}

func TestPostIPLiteralSkipsResolver(t *testing.T) {
	a := newReplica(t, 200)
	res := &fakeResolver{err: errors.New("must not be called")}
	client := &AlertmanagerClient{URL: a.server.URL, HTTPClient: a.server.Client(), Resolver: res}

	require.NoError(t, client.Post(testAlert()))
	assert.Equal(t, 0, res.calls)
}

func TestPostResolveCacheAndFailureFallback(t *testing.T) {
	a, b := newReplica(t, 200), newReplica(t, 200)
	client, res, _ := clientFor(a, b)

	now := time.Now()
	client.now = func() time.Time { return now }

	require.NoError(t, client.Post(testAlert()))
	require.NoError(t, client.Post(testAlert()))
	assert.Equal(t, 1, res.calls, "answer cached within the TTL")

	now = now.Add(resolveCacheTTL + time.Second)
	res.err = errors.New("dns down")

	require.NoError(t, client.Post(testAlert()))
	assert.Equal(t, 2, res.calls)
	assert.Equal(t, 3, a.hits, "a failed lookup reuses the last good answer")
	assert.Equal(t, 3, b.hits)
}

func plainCounter(c prometheus.Counter) float64 {
	var m dto.Metric

	_ = c.Write(&m)

	return m.GetCounter().GetValue()
}
