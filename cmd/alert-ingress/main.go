package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	configPath := flag.String("config", "/etc/alert-ingress/config.yaml",
		"path to the configuration the chart rendered into a ConfigMap")
	addr := flag.String("listen-addr", ":8080",
		"address the webhook, /healthz and /metrics are served on")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		logger.Error("loading configuration", "error", err)
		os.Exit(1)
	}

	registry := prometheus.NewRegistry()
	metrics := NewMetrics(registry)

	srv := &Server{
		Config:   cfg,
		Verifier: NewVerifier(nil),
		Alertmanager: &AlertmanagerClient{
			URL:        cfg.Alertmanager.URL,
			HTTPClient: &http.Client{Timeout: 10 * time.Second},
		},
		Metrics: metrics,
		Logger:  logger,
	}

	mux := http.NewServeMux()
	mux.Handle("/", srv)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))

	logger.Info("alert-ingress listening", "addr", *addr)

	server := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	if err := server.ListenAndServe(); err != nil {
		logger.Error("serving", "error", err)
		os.Exit(1)
	}
}
