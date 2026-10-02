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
		"address the webhook is served on; with no -metrics-addr it also serves /healthz and /metrics")
	metricsAddr := flag.String("metrics-addr", "",
		"when set, /healthz and /metrics are served ONLY on this address and never on -listen-addr, so a route to the webhook port cannot expose them")
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

	handler, opsHandler := newHandlers(srv, registry, *metricsAddr != "")

	if *metricsAddr != "" {
		ops := &http.Server{
			Addr:              *metricsAddr,
			Handler:           opsHandler,
			ReadHeaderTimeout: 5 * time.Second,
		}

		go func() {
			logger.Info("alert-ingress metrics listening", "addr", *metricsAddr)

			if err := ops.ListenAndServe(); err != nil {
				logger.Error("serving metrics", "error", err)
				os.Exit(1)
			}
		}()
	}

	logger.Info("alert-ingress listening", "addr", *addr)

	server := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	if err := server.ListenAndServe(); err != nil {
		logger.Error("serving", "error", err)
		os.Exit(1)
	}
}

// newHandlers builds the webhook handler and the operational one
// (/healthz, /metrics). With separate=false they are one mux, the
// original single-port layout, and opsHandler is nil. With separate=true
// the webhook handler is the Server alone — it answers nothing but the
// webhook, so no path on that port can reach metrics — and opsHandler
// serves the operational endpoints for a second listener.
func newHandlers(srv http.Handler, registry *prometheus.Registry, separate bool) (handler, opsHandler http.Handler) {
	ops := http.NewServeMux()
	ops.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	ops.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))

	if separate {
		return srv, ops
	}

	mux := http.NewServeMux()
	mux.Handle("/", srv)
	mux.Handle("/healthz", ops)
	mux.Handle("/metrics", ops)

	return mux, nil
}
