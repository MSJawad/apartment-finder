package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Check is a named readiness check, such as pinging the database.
type Check struct {
	Name string
	Run  func(context.Context) error
}

// AdminHandler serves the endpoints for Kubernetes probes and Prometheus. It
// runs on its own port that the ingress never routes to.
//
//   - /healthz: the process is running (liveness probe)
//   - /readyz: every check passes, so it may receive traffic (readiness probe)
//   - /metrics: everything registered in reg
//
// Requests here are not logged: probes and scrapes arrive every few seconds
// and would drown out real traffic.
func AdminHandler(reg *prometheus.Registry, logger *slog.Logger, checks []Check) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		for _, check := range checks {
			if err := check.Run(r.Context()); err != nil {
				logger.LogAttrs(r.Context(), slog.LevelWarn, "readiness check failed",
					slog.String("check", check.Name), slog.Any("error", err))
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "failed": check.Name})
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{Registry: reg}))
	return mux
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// The status is already sent; an encode error means the client went away.
	_ = json.NewEncoder(w).Encode(body)
}
