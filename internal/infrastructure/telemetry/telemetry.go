// Package telemetry — журналы в JSON и реестр метрик Prometheus.
package telemetry

import (
	"io"
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// NewLogger — журнал в JSON с уровнем из конфига.
func NewLogger(level string, out io.Writer) *slog.Logger {
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

	return slog.New(slog.NewJSONHandler(out, &slog.HandlerOptions{Level: lvl})).
		With(slog.String("service", "marketplace"))
}

// NewRegistry — реестр метрик со сборщиками процесса и рантайма Go.
func NewRegistry() *prometheus.Registry {
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	return registry
}
