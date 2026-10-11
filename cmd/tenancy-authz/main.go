// Command tenancy-authz is an Envoy external authorisation service that
// turns a person's group membership into the filters vmauth enforces.
//
// vmauth selects ONE user per token by its claims, so a person who holds two
// groups reads whatever the first matching group allows and never both. It
// can template filters only from a `vm_access` claim in the token, and the
// identity provider's tokens carry groups, not store query syntax. This
// service stands between the two. Envoy asks it about every request on the
// read route; for a person's token it verifies the token against the
// identity provider, unions the grants of every principal the person's
// groups select, and answers with an Authorization header carrying a short
// lived token of its own that holds exactly that union as `vm_access`.
// vmauth trusts only this service's issuer for that claim and stays the
// enforcement point.
//
// A token that is not a person's is passed through untouched.
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
	configPath := flag.String("config", "/etc/tenancy-authz/config.yaml",
		"path to the configuration the chart rendered into a ConfigMap")
	listen := flag.String("listen", ":8080",
		"address of the check, discovery and key endpoints")
	adminListen := flag.String("admin-listen", ":9090",
		"address of /metrics")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("version", version)

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		logger.Error("loading configuration", "error", err.Error())
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Superseded keys stay published for as long as a token they signed can
	// live, and a margin.
	keyring, err := NewKeyring(cfg.Authz.SigningKey.File, cfg.Authz.TokenTTL+10*time.Minute)
	if err != nil {
		logger.Error("loading the signing key", "error", err.Error())
		os.Exit(1)
	}
	logger.Info("signing key loaded", "kid", keyring.KeyID())
	go keyring.Run(ctx,
		func(kid string) {
			logger.Info("signing key changed: published now, signing after the switch delay", "kid", kid)
		},
		func(err error) { logger.Warn("reloading the signing key; keeping the keys held", "error", err.Error()) })

	registry := prometheus.NewRegistry()
	metrics := NewMetrics(registry)

	verifier := NewVerifier(cfg.Authz.UpstreamIssuer, cfg.ClaimName, cfg.Authz.Leeway, nil)
	verifier.onRefresh = func(ok bool) {
		result := "error"
		if ok {
			result = "ok"
		}
		metrics.Refreshes.WithLabelValues(result).Inc()
	}
	go verifier.Run(ctx, logger)

	authz := NewAuthorizer(cfg, verifier, keyring, metrics, logger)
	ready := func() bool { return verifier.Ready() }

	mux := newMux(cfg, keyring.JWKS, authz)
	admin := http.NewServeMux()
	admin.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	admin.HandleFunc("/readyz", readyHandler(ready))
	admin.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	// The probes are on the serving port too: the chart's probes target it,
	// and a pod that answers its probes on a port nothing else uses would
	// pass them while the port that matters is wedged.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/readyz", readyHandler(ready))

	go func() {
		for ctx.Err() == nil {
			if ready() {
				metrics.Ready.Set(1)
			} else {
				metrics.Ready.Set(0)
			}
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
		}
	}()

	servers := []*http.Server{
		{Addr: *listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second},
		{Addr: *adminListen, Handler: admin, ReadHeaderTimeout: 5 * time.Second},
	}
	errs := make(chan error, len(servers))
	for _, s := range servers {
		go func() {
			logger.Info("listening", "addr", s.Addr)
			errs <- s.ListenAndServe()
		}()
	}

	failed := false
	select {
	case <-ctx.Done():
	case err := <-errs:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("serving", "error", err.Error())
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

func readyHandler(ready func() bool) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if !ready() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

// newMux serves the check, the discovery document and the key set.
func newMux(cfg Config, jwks func() []byte, authz http.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle(checkPrefix, authz)
	mux.Handle(checkPrefix+"/", authz)

	issuer := cfg.Authz.IssuerURL
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "max-age=300")
		_, _ = w.Write(discoveryDocument(issuer))
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "max-age=300")
		_, _ = w.Write(jwks())
	})
	return mux
}
