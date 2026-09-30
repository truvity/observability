// Command mcp-aggregator serves the allowlisted tools of several stock MCP
// servers as one MCP server.
//
// It is generic: a YAML file lists backends (a prefix, a loopback
// streamable-HTTP endpoint, the upstream tool names to expose). It connects
// to each at start, keeps the allowlisted tools with their descriptions and
// schemas verbatim, exposes them as <prefix>_<tool>, and routes each
// tools/call to the backend that owns it. It advertises tools and nothing
// else, holds no credential, and forwards no caller identity: it sits behind
// a resource-proxy that has already validated the caller.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// version is stamped by the release build.
var version = "dev"

func main() {
	configPath := flag.String("config", "/etc/mcp-aggregator/config.yaml",
		"path to the configuration the chart rendered into a ConfigMap")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		logger.Error("loading configuration", "error", err)
		os.Exit(1)
	}

	registry := prometheus.NewRegistry()
	agg := NewAggregator(cfg, logger, NewMetrics(registry))
	agg.Version = version

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)

	mux := http.NewServeMux()
	mux.Handle(cfg.Path, agg)
	admin := http.NewServeMux()
	admin.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	admin.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !agg.Ready() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	admin.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))

	servers := []*http.Server{
		{Addr: cfg.Listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second},
		{Addr: cfg.AdminListen, Handler: admin, ReadHeaderTimeout: 5 * time.Second},
	}
	errs := make(chan error, len(servers))
	for _, s := range servers {
		go func() {
			logger.Info("listening", "addr", s.Addr)
			errs <- s.ListenAndServe()
		}()
	}

	go agg.Run(ctx)

	failed := false
	select {
	case <-ctx.Done():
	case err := <-errs:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("serving", "error", err)
			failed = true
		}
	}
	stop()
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	for _, s := range servers {
		_ = s.Shutdown(shutdown)
	}
	cancel()
	if failed {
		os.Exit(1)
	}
}
