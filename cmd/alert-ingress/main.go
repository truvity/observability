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

	// In sqs mode the webhook is off: the port still serves /healthz and
	// /metrics, and answers 404 to anything else.
	var webhook http.Handler = srv
	if !cfg.Input.HTTP() {
		webhook = http.NotFoundHandler()
	}

	handler, opsHandler := newHandlers(webhook, registry, *metricsAddr != "")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)

	if cfg.Input.UsesSQS() {
		client, err := NewSQSClient(ctx, cfg.Input.SQS.Region)
		if err != nil {
			logger.Error("creating the SQS client", "error", err)
			os.Exit(1)
		}

		consumer := &SQSConsumer{
			Client:       client,
			Config:       cfg.Input.SQS,
			Server:       srv,
			Metrics:      metrics,
			Logger:       logger,
			ErrorBackoff: 5 * time.Second,
		}

		logger.Info("alert-ingress polling the queue", "queue", cfg.Input.SQS.QueueURL, "mode", cfg.Input.Mode)

		go consumer.Run(ctx)
	}

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

	go func() {
		<-ctx.Done()
		stop()

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		_ = server.Shutdown(shutdownCtx)
	}()

	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
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
