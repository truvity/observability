package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// resolveCacheTTL is how long the addresses behind the Alertmanager host
// are reused before the name is looked up again.
const resolveCacheTTL = 30 * time.Second

// Resolver looks a host name up. *net.Resolver satisfies it; tests inject
// a fake so nothing here touches real DNS.
type Resolver interface {
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// AlertmanagerAlert is one entry of the array Alertmanager's
// POST /api/v2/alerts accepts. EndsAt is always set: cloud events do not
// resolve themselves, so the alert expires on a timer rather than
// lingering in Alertmanager forever waiting for a resolution that will
// never arrive.
type AlertmanagerAlert struct {
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	StartsAt    time.Time         `json:"startsAt"`
	EndsAt      time.Time         `json:"endsAt"`
}

// AlertmanagerClient is the one thing this service is allowed to reach.
// The chart's NetworkPolicy enforces that at the network layer; this
// struct enforces it in the code by being the only outbound call in
// handler.go's success path.
//
// An HA Alertmanager (two or more replicas in a gossip cluster) shares
// silences and the notification log but NOT alerts: every client must
// post each alert to every replica. When URL's host resolves to several
// addresses (a headless Service, which is what the vm-operator creates),
// Post sends the alert to each of them. A host that resolves to one
// address, or an IP literal, gets one POST to URL exactly as written.
type AlertmanagerClient struct {
	URL        string
	HTTPClient *http.Client

	// Resolver defaults to net.DefaultResolver.
	Resolver Resolver
	// Failures, when set, counts each replica POST that failed, whether
	// or not another replica accepted the alert.
	Failures prometheus.Counter
	Logger   *slog.Logger

	mu       sync.Mutex
	cached   []string
	cachedAt time.Time
	now      func() time.Time
}

func (c *AlertmanagerClient) clock() time.Time {
	if c.now != nil {
		return c.now()
	}

	return time.Now()
}

// targets returns the URLs to post to, each with the Host header to send.
func (c *AlertmanagerClient) targets() ([]target, error) {
	u, err := url.Parse(c.URL)
	if err != nil {
		return nil, fmt.Errorf("parsing alertmanager url: %w", err)
	}

	single := []target{{url: c.URL + "/api/v2/alerts"}}

	host := u.Hostname()
	if host == "" || net.ParseIP(host) != nil {
		return single, nil
	}

	addrs := c.lookup(host)
	if len(addrs) < 2 {
		return single, nil
	}

	port := u.Port()
	out := make([]target, 0, len(addrs))

	for _, a := range addrs {
		v := *u
		v.Host = a
		if port != "" {
			v.Host = net.JoinHostPort(a, port)
		} else if net.ParseIP(a).To4() == nil {
			v.Host = "[" + a + "]"
		}

		out = append(out, target{url: v.String() + "/api/v2/alerts", host: u.Host})
	}

	return out, nil
}

type target struct {
	url  string
	host string // Host header override; empty means the URL's own
}

// lookup resolves host, caching the answer for resolveCacheTTL. A failed
// lookup reuses the last good answer; with none, it returns nil and the
// caller posts to the name, as a single-address URL.
func (c *AlertmanagerClient) lookup(host string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cached != nil && c.clock().Sub(c.cachedAt) < resolveCacheTTL {
		return c.cached
	}

	r := c.Resolver
	if r == nil {
		r = net.DefaultResolver
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	addrs, err := r.LookupHost(ctx, host)
	if err != nil || len(addrs) == 0 {
		if c.Logger != nil {
			c.Logger.Warn("resolving alertmanager host; using the previous answer or the name itself", "error", err)
		}

		return c.cached
	}

	c.cached, c.cachedAt = addrs, c.clock()

	return addrs
}

// Post sends alert to every Alertmanager replica concurrently, each bounded
// by HTTPClient's timeout. It succeeds when at least one replica answered
// 2xx: a replica that is down for hours must not turn every message into a
// retry and finally a dead letter. Each failed replica is logged and
// counted. Only when every replica failed is an error returned, so the
// caller does not count the message as delivered and the provider's own
// retry recovers it.
func (c *AlertmanagerClient) Post(alert AlertmanagerAlert) error {
	payload, err := json.Marshal([]AlertmanagerAlert{alert})
	if err != nil {
		return fmt.Errorf("encoding alert: %w", err)
	}

	targets, err := c.targets()
	if err != nil {
		return err
	}

	errs := make([]error, len(targets))

	var wg sync.WaitGroup

	for i, t := range targets {
		wg.Add(1)

		go func() {
			defer wg.Done()

			errs[i] = c.postOne(t, payload)
		}()
	}

	wg.Wait()

	failed := 0

	for _, e := range errs {
		if e == nil {
			continue
		}

		failed++

		if c.Failures != nil {
			c.Failures.Inc()
		}

		if c.Logger != nil && failed < len(errs) {
			c.Logger.Warn("posting to one alertmanager replica failed; another accepted the alert", "error", e)
		}
	}

	if failed == len(errs) {
		return errors.Join(errs...)
	}

	return nil
}

// postOne POSTs payload to one replica. A non-2xx or a transport error is
// returned rather than swallowed.
func (c *AlertmanagerClient) postOne(t target, payload []byte) error {
	req, err := http.NewRequest(http.MethodPost, t.url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}

	if t.host != "" {
		req.Host = t.host
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("posting to alertmanager: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("alertmanager answered %d", resp.StatusCode)
	}

	return nil
}
