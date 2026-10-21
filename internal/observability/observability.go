// Package observability sets up structured logging and Prometheus metrics.
package observability

import (
	"io"
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// NewLogger returns a JSON logger that tags every line with the service name
// and build version, so lines from the old and new pods in a rollout can be
// told apart.
func NewLogger(w io.Writer, level slog.Level, service, version string) *slog.Logger {
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	return slog.New(handler).With("service", service, "version", version)
}

// NewRegistry returns a registry with Go runtime and process metrics plus a
// build info gauge. Using our own registry instead of the global default keeps
// tests isolated and makes every exported metric explicit.
func NewRegistry(version string) *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name:        "apartment_finder_build_info",
			Help:        "Always 1; the version label says which build is running.",
			ConstLabels: prometheus.Labels{"version": version},
		}, func() float64 { return 1 }),
	)
	return reg
}
