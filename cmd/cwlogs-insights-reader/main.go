// Command cwlogs-insights-reader reads a CloudWatch Logs group with Logs
// Insights and hands the events to the OpenTelemetry collector beside it.
//
// It exists for log groups of the Infrequent Access class, which have no
// GetLogEvents, FilterLogEvents or subscription filters: Logs Insights
// (StartQuery, GetQueryResults) is the only way to read them. The chart's
// `cloudwatchLogs.readMode: insights` runs it in the reader pod in place of the
// collector's `awscloudwatch` receiver; the collector keeps the filtering,
// field lifting, tenancy stamp and export, so both modes produce the same
// records.
//
// The query is a coarse prefilter (stream prefixes and cheap text tests); the
// collector's processors do the final matching.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// version is stamped by the release build.
var version = "dev"

// Config is the command line.
type Config struct {
	Region       string
	LogGroup     string
	Streams      stringList
	Filter       string
	OTLPURL      string
	StatePath    string
	PollInterval time.Duration
	Overlap      time.Duration
	MaxWindow    time.Duration
	InitialLB    time.Duration
	MetricsAddr  string
}

type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }

func (l *stringList) Set(v string) error {
	*l = append(*l, v)

	return nil
}

// ParseFlags reads the command line.
func ParseFlags(fs *flag.FlagSet, args []string) (*Config, error) {
	c := &Config{}

	fs.StringVar(&c.Region, "region", "", "AWS region of the log group")
	fs.StringVar(&c.LogGroup, "log-group", "", "log group name")
	fs.Var(&c.Streams, "stream-prefix", "stream name prefix to read (repeatable)")
	fs.StringVar(&c.Filter, "query-filter", "", "Logs Insights filter expression ANDed after the stream filter")
	fs.StringVar(&c.OTLPURL, "otlp-url", "http://127.0.0.1:4318/v1/logs", "collector OTLP/HTTP logs endpoint")
	fs.StringVar(&c.StatePath, "state-file", "/var/lib/otelcol/insights/state.json", "watermark and dedup state")
	fs.DurationVar(&c.PollInterval, "poll-interval", time.Minute, "time between polls")
	fs.DurationVar(&c.Overlap, "overlap", 3*time.Minute, "how far before the watermark each window starts (delivery delay tolerance)")
	fs.DurationVar(&c.MaxWindow, "max-window", 15*time.Minute, "longest window in one query when catching up")
	fs.DurationVar(&c.InitialLB, "initial-lookback", 10*time.Minute, "how far back the first start reads")
	fs.StringVar(&c.MetricsAddr, "metrics-addr", ":8889", "metrics listen address")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	return c, c.Check()
}

// Check refuses a configuration that cannot work.
func (c *Config) Check() error {
	var errs []error

	if c.Region == "" {
		errs = append(errs, errors.New("--region is required"))
	}

	if !strings.HasPrefix(c.LogGroup, "/") {
		errs = append(errs, errors.New("--log-group must be a full log group name starting with /"))
	}

	if len(c.Streams) == 0 {
		errs = append(errs, errors.New("at least one --stream-prefix is required"))
	}

	if c.PollInterval <= 0 || c.Overlap < 0 || c.MaxWindow < time.Minute || c.InitialLB <= 0 {
		errs = append(errs, errors.New("--poll-interval, --max-window and --initial-lookback must be positive (max-window at least 1m) and --overlap not negative"))
	}

	return errors.Join(errs...)
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := ParseFlags(flag.CommandLine, os.Args[1:])
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("version", version)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	awsCfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(cfg.Region))
	if err != nil {
		return fmt.Errorf("aws config: %w", err)
	}

	registry := prometheus.NewRegistry()
	metrics := NewMetrics(registry)

	state, err := LoadState(cfg.StatePath)
	if err != nil {
		return fmt.Errorf("state: %w", err)
	}

	poller := &Poller{
		Source: &Fetcher{
			API: cloudwatchlogs.NewFromConfig(awsCfg), LogGroup: cfg.LogGroup, Streams: cfg.Streams, Filter: cfg.Filter,
			Metrics: metrics, PollEvery: 2 * time.Second, Retries: 8, Backoff: 2 * time.Second, Sleep: sleep,
		},
		Sink: &OTLPSink{
			URL: cfg.OTLPURL, Region: cfg.Region, LogGroup: cfg.LogGroup,
			HTTP: &http.Client{Timeout: 30 * time.Second}, Retries: 10, Sleep: sleep,
		},
		StatePath: cfg.StatePath, State: state, Overlap: cfg.Overlap, MaxWindow: cfg.MaxWindow,
		InitialLB: cfg.InitialLB, Metrics: metrics, Logger: logger,
	}

	admin := http.NewServeMux()
	admin.Handle("GET /metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))

	srv := &http.Server{Addr: cfg.MetricsAddr, Handler: admin, ReadHeaderTimeout: 10 * time.Second}

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("metrics server", "error", err.Error())
		}
	}()

	logger.Info("started", "group", cfg.LogGroup, "streams", cfg.Streams, "watermark", state.Watermark)

	for {
		if err := poller.Poll(ctx, time.Now()); err != nil && ctx.Err() == nil {
			// The state was not advanced: the next poll repeats the window.
			logger.Error("poll", "error", err.Error())
		}

		if sleep(ctx, cfg.PollInterval) != nil {
			break
		}
	}

	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_ = srv.Shutdown(shutdown)

	return nil
}
