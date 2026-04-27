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
	"regexp"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/time/rate"
)

var (
	version  = "dev"
	revision = "unknown"
)

func main() {
	var (
		listenAddr    = flag.String("web.listen-address", ":9144", "Address to listen on for HTTP requests.")
		metricsPath   = flag.String("web.metrics-path", "/metrics", "Path under which to expose metrics.")
		deviceInclude = flag.String("neighbor.device-include", "", "Regex of devices to include (empty = all).")
		deviceExclude = flag.String("neighbor.device-exclude", "", "Regex of devices to exclude (empty = none).")
		syncInterval  = flag.Duration("neighbor.sync-interval", 15*time.Minute, "Interval for kernel neighbor table resync and stale entry purge.")
		deleteGrace   = flag.Duration("neighbor.delete-grace", 30*time.Second, "Grace period to retain MAC after entry deletion.")
		flapBurst     = flag.Int("neighbor.flap-burst", 5, "Max flap events per key before rate limiting.")
		logLevel      = flag.String("log.level", "info", "Log level (debug, info, warn, error).")
		logFormat     = flag.String("log.format", "logfmt", "Log format (logfmt, json).")
		showVersion   = flag.Bool("version", false, "Show version and exit.")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("ipneigh_exporter version=%s revision=%s\n", version, revision)
		os.Exit(0)
	}

	logger := newLogger(*logLevel, *logFormat)

	var includeRe, excludeRe *regexp.Regexp
	if *deviceInclude != "" {
		includeRe = regexp.MustCompile(*deviceInclude)
	}
	if *deviceExclude != "" {
		excludeRe = regexp.MustCompile(*deviceExclude)
	}

	config := StoreConfig{
		SyncInterval:  *syncInterval,
		DeleteGrace:   *deleteGrace,
		FlapRate:      rate.Limit(1),
		FlapBurst:     *flapBurst,
		DeviceInclude: includeRe,
		DeviceExclude: excludeRe,
	}

	store := NewNeighborStore(config, &NetlinkResolver{}, logger)
	source := NewNetlinkSource(logger, store.RecordError)
	watcher := NewWatcher(source, store, logger)
	collector := NewNeighborCollector(store)

	reg := prometheus.NewRegistry()
	reg.MustRegister(collector)
	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	reg.MustRegister(collectors.NewBuildInfoCollector())

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	watcherErrCh := make(chan error, 1)
	go func() {
		if err := watcher.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Error("watcher failed", "error", err)
			watcherErrCh <- err
			cancel()
		}
	}()

	mux := http.NewServeMux()
	mux.Handle("GET "+*metricsPath, promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if watcher.Ready() {
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "ok")
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, "not ready")
		}
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<html><body>
<h1>ipneigh_exporter</h1>
<p><a href="%s">Metrics</a></p>
</body></html>`, *metricsPath)
	})

	server := &http.Server{Addr: *listenAddr, Handler: mux}

	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("http server shutdown error", "error", err)
		}
	}()

	logger.Info("starting ipneigh_exporter",
		"listen", *listenAddr, "version", version, "revision", revision)
	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		logger.Error("http server error", "error", err)
		os.Exit(1)
	}
	select {
	case <-watcherErrCh:
		os.Exit(1)
	default:
	}
}

func newLogger(level, format string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: lvl}

	var handler slog.Handler
	if format == "json" {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		handler = slog.NewTextHandler(os.Stderr, opts)
	}

	return slog.New(handler)
}
