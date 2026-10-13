// Command telegram-relay turns GoAlert's outgoing webhook into a Telegram
// message from one bot to one person.
//
// GoAlert has no Telegram contact method, but every user can add a
// "Webhook" contact method: GoAlert POSTs a small JSON document (an alert,
// a status update, a verification code, a test) to the URL the user gave.
// This service is that URL. The path names a recipient
// (`/v1/notify/<recipient>`); the recipient's Telegram chat id comes from a
// file the estate mounts, never from the request, so a GoAlert user cannot
// make the bot write to a chat nobody configured.
//
// Every request must carry the shared secret (as `?token=`, as the password
// of HTTP Basic auth in the URL, or as a bearer token): GoAlert sends no
// signature of its own, and the relay's port is reachable from GoAlert's
// pods only, but a NetworkPolicy is not a credential.
//
// GoAlert gives a webhook three seconds and does not read the answer, so
// the relay sends synchronously within a shorter budget and reports a
// failure the only place anyone will see it: its own metrics.
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
	cfg, err := ParseFlags(flag.CommandLine, os.Args[1:], os.LookupEnv)
	if err != nil {
		slog.Error("configuration", "error", err.Error())
		os.Exit(2)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("version", version)
	if err := cfg.Check(); err != nil {
		logger.Error("configuration", "error", err.Error())
		os.Exit(1)
	}

	registry := prometheus.NewRegistry()
	metrics := NewMetrics(registry)

	relay := &Relay{
		Secrets:  FileSecrets{BotTokenFile: cfg.BotTokenFile, SharedSecretFile: cfg.SharedSecretFile, RecipientsFile: cfg.RecipientsFile},
		Telegram: &TelegramClient{BaseURL: cfg.TelegramAPIURL, HTTP: &http.Client{}},
		Timeout:  cfg.SendTimeout,
		Metrics:  metrics,
		Logger:   logger,
	}

	api := http.NewServeMux()
	api.Handle("POST /v1/notify/{recipient}", relay)

	admin := http.NewServeMux()
	admin.Handle("GET /metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	admin.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	admin.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		// Ready means the three mounted files are readable and well formed
		// right now: a Secret that never arrived keeps the pod out of the
		// Service instead of answering every page with an error.
		if err := relay.Secrets.Check(); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)

	servers := []*http.Server{
		{Addr: cfg.Listen, Handler: api, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second},
		{Addr: cfg.AdminListen, Handler: admin, ReadHeaderTimeout: 5 * time.Second},
	}
	errs := make(chan error, len(servers))
	for _, srv := range servers {
		go func() {
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errs <- err
			}
		}()
	}
	logger.Info("listening", "listen", cfg.Listen, "admin", cfg.AdminListen)

	select {
	case <-ctx.Done():
		stop()
	case err := <-errs:
		logger.Error("server", "error", err.Error())
		stop()
		os.Exit(1)
	}

	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, srv := range servers {
		_ = srv.Shutdown(shutdown)
	}
}
