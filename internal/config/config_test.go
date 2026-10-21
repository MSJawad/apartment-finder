package config

import (
	"log/slog"
	"testing"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    Config
		wantErr bool
	}{
		{
			name: "defaults",
			env:  map[string]string{},
			want: Config{Addr: ":8080", AdminAddr: ":9090", LogLevel: slog.LevelInfo},
		},
		{
			name: "overrides",
			env:  map[string]string{"ADDR": ":8000", "ADMIN_ADDR": "127.0.0.1:9000", "LOG_LEVEL": "debug"},
			want: Config{Addr: ":8000", AdminAddr: "127.0.0.1:9000", LogLevel: slog.LevelDebug},
		},
		{
			name: "log level is case insensitive",
			env:  map[string]string{"LOG_LEVEL": "WARN"},
			want: Config{Addr: ":8080", AdminAddr: ":9090", LogLevel: slog.LevelWarn},
		},
		{
			name:    "unknown log level",
			env:     map[string]string{"LOG_LEVEL": "loud"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Load(func(key string) string { return tt.env[key] })
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Load() = %+v, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("Load() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
