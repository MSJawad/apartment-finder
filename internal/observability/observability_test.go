package observability

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestNewLoggerWritesJSONWithServiceAndVersion(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, slog.LevelInfo, "api", "abc123")

	logger.Debug("hidden")
	logger.Info("started", "addr", ":8080")

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("want exactly one JSON line (debug filtered out), got %q: %v", buf.String(), err)
	}
	want := map[string]any{"level": "INFO", "msg": "started", "service": "api", "version": "abc123", "addr": ":8080"}
	for key, value := range want {
		if line[key] != value {
			t.Errorf("%s = %v, want %v", key, line[key], value)
		}
	}
}

func TestNewRegistryExportsBuildInfo(t *testing.T) {
	families, err := NewRegistry("abc123").Gather()
	if err != nil {
		t.Fatalf("Gather() error = %v", err)
	}
	for _, family := range families {
		if family.GetName() != "apartment_finder_build_info" {
			continue
		}
		metric := family.GetMetric()[0]
		if got := metric.GetGauge().GetValue(); got != 1 {
			t.Errorf("value = %v, want 1", got)
		}
		if label := metric.GetLabel()[0]; label.GetName() != "version" || label.GetValue() != "abc123" {
			t.Errorf("label = %s=%s, want version=abc123", label.GetName(), label.GetValue())
		}
		return
	}
	t.Fatal("apartment_finder_build_info not exported")
}
