package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestObserve(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantRoute  string
		wantMethod string // the metric label, not the raw method
	}{
		{name: "matched route with body", method: http.MethodGet, path: "/listings/42",
			wantStatus: http.StatusOK, wantRoute: "/listings/{id}", wantMethod: "GET"},
		{name: "handler writes nothing", method: http.MethodPost, path: "/listings",
			wantStatus: http.StatusOK, wantRoute: "/listings", wantMethod: "POST"},
		{name: "explicit status", method: http.MethodDelete, path: "/listings/42",
			wantStatus: http.StatusNoContent, wantRoute: "/listings/{id}", wantMethod: "DELETE"},
		{name: "unknown path", method: http.MethodGet, path: "/no/such/page",
			wantStatus: http.StatusNotFound, wantRoute: "unmatched", wantMethod: "GET"},
		{name: "wrong method on known path", method: http.MethodPut, path: "/listings",
			wantStatus: http.StatusMethodNotAllowed, wantRoute: "unmatched", wantMethod: "PUT"},
		{name: "nonstandard method", method: "BREW", path: "/listings",
			wantStatus: http.StatusMethodNotAllowed, wantRoute: "unmatched", wantMethod: "other"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			reg := prometheus.NewRegistry()
			metrics := NewMetrics(reg)
			handler := Observe(testMux(), metrics, slog.New(slog.NewJSONHandler(&logs, nil)))

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), tt.method, tt.path, nil))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}

			var line struct {
				Method     string  `json:"method"`
				Route      string  `json:"route"`
				Path       string  `json:"path"`
				Status     int     `json:"status"`
				Bytes      int     `json:"bytes"`
				DurationMS float64 `json:"duration_ms"`
			}
			if err := json.Unmarshal(logs.Bytes(), &line); err != nil {
				t.Fatalf("want one JSON log line, got %q: %v", logs.String(), err)
			}
			if line.Method != tt.method || line.Route != tt.wantRoute || line.Path != tt.path ||
				line.Status != tt.wantStatus || line.Bytes != rec.Body.Len() || line.DurationMS < 0 {
				t.Errorf("log line = %+v, want method %s route %s path %s status %d bytes %d",
					line, tt.method, tt.wantRoute, tt.path, tt.wantStatus, rec.Body.Len())
			}

			count := testutil.ToFloat64(metrics.requests.WithLabelValues(tt.wantMethod, tt.wantRoute, strconv.Itoa(tt.wantStatus)))
			if count != 1 {
				t.Errorf("http_requests_total{method=%q,route=%q,code=\"%d\"} = %v, want 1",
					tt.wantMethod, tt.wantRoute, tt.wantStatus, count)
			}
			if series := testutil.CollectAndCount(metrics.duration); series != 1 {
				t.Errorf("duration series = %d, want 1", series)
			}
		})
	}
}

func testMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /listings/{id}", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("a listing"))
	})
	mux.HandleFunc("POST /listings", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("DELETE /listings/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}
