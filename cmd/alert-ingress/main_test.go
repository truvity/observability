package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
)

func get(h http.Handler, path string) int {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

	return rec.Code
}

// The default layout is unchanged: one port serves the webhook, /healthz
// and /metrics.
func TestSinglePortServesOperationalEndpoints(t *testing.T) {
	webhook := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h, ops := newHandlers(webhook, prometheus.NewRegistry(), false)

	assert.Nil(t, ops)
	assert.Equal(t, http.StatusOK, get(h, "/healthz"))
	assert.Equal(t, http.StatusOK, get(h, "/metrics"))
	assert.Equal(t, http.StatusTeapot, get(h, "/"))
}

// With a separate metrics address, the webhook port never answers
// /metrics or /healthz itself: everything on it is the webhook handler.
func TestSeparatePortKeepsMetricsOffTheWebhookPort(t *testing.T) {
	webhook := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h, ops := newHandlers(webhook, prometheus.NewRegistry(), true)

	assert.Equal(t, http.StatusTeapot, get(h, "/metrics"), "the webhook port hands /metrics to the webhook handler")
	assert.Equal(t, http.StatusTeapot, get(h, "/healthz"))
	assert.Equal(t, http.StatusOK, get(ops, "/metrics"))
	assert.Equal(t, http.StatusOK, get(ops, "/healthz"))
	assert.Equal(t, http.StatusNotFound, get(ops, "/"))
}
