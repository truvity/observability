package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

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
type AlertmanagerClient struct {
	URL        string
	HTTPClient *http.Client
}

// Post sends alert. A non-2xx or a transport error is returned rather
// than swallowed, so the caller can decide not to count the message as
// delivered — the provider's own retry is what recovers a transient
// Alertmanager outage, and a message this service claims to have handled
// but did not deliver is a gap nobody would otherwise see.
func (c *AlertmanagerClient) Post(alert AlertmanagerAlert) error {
	payload, err := json.Marshal([]AlertmanagerAlert{alert})
	if err != nil {
		return fmt.Errorf("encoding alert: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, c.URL+"/api/v2/alerts", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
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
