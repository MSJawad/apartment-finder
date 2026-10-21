package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestAdminHandler(t *testing.T) {
	passing := Check{Name: "cache", Run: func(context.Context) error { return nil }}
	failing := Check{Name: "database", Run: func(context.Context) error { return errors.New("connection refused") }}

	tests := []struct {
		name       string
		path       string
		checks     []Check
		wantStatus int
		wantBody   string
	}{
		{name: "liveness ignores readiness checks", path: "/healthz", checks: []Check{failing},
			wantStatus: http.StatusOK, wantBody: `{"status":"ok"}`},
		{name: "ready with no checks", path: "/readyz",
			wantStatus: http.StatusOK, wantBody: `{"status":"ok"}`},
		{name: "ready when every check passes", path: "/readyz", checks: []Check{passing},
			wantStatus: http.StatusOK, wantBody: `{"status":"ok"}`},
		{name: "not ready names the failed check", path: "/readyz", checks: []Check{passing, failing},
			wantStatus: http.StatusServiceUnavailable, wantBody: `"failed":"database"`},
		{name: "metrics from the registry", path: "/metrics",
			wantStatus: http.StatusOK, wantBody: "test_events_total 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := prometheus.NewRegistry()
			events := prometheus.NewCounter(prometheus.CounterOpts{Name: "test_events_total", Help: "Test counter."})
			reg.MustRegister(events)
			events.Inc()
			handler := AdminHandler(reg, slog.New(slog.DiscardHandler), tt.checks)

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, tt.path, nil))

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if body := rec.Body.String(); !strings.Contains(body, tt.wantBody) {
				t.Errorf("body = %q, want it to contain %q", body, tt.wantBody)
			}
		})
	}
}
