package config

import (
	"log/slog"
	"testing"
)

func TestLoad(t *testing.T) {
	const dbURL = "postgres://app@db:5432/app"
	tests := []struct {
		name    string
		env     map[string]string
		want    Config
		wantErr bool
	}{
		{
			name: "defaults",
			env:  map[string]string{"DATABASE_URL": dbURL},
			want: Config{Addr: ":8080", AdminAddr: ":9090", LogLevel: slog.LevelInfo, DatabaseURL: dbURL},
		},
		{
			name: "overrides",
			env:  map[string]string{"DATABASE_URL": dbURL, "ADDR": ":8000", "ADMIN_ADDR": "127.0.0.1:9000", "LOG_LEVEL": "debug"},
			want: Config{Addr: ":8000", AdminAddr: "127.0.0.1:9000", LogLevel: slog.LevelDebug, DatabaseURL: dbURL},
		},
		{
			name: "log level is case insensitive",
			env:  map[string]string{"DATABASE_URL": dbURL, "LOG_LEVEL": "WARN"},
			want: Config{Addr: ":8080", AdminAddr: ":9090", LogLevel: slog.LevelWarn, DatabaseURL: dbURL},
		},
		{
			name:    "unknown log level",
			env:     map[string]string{"DATABASE_URL": dbURL, "LOG_LEVEL": "loud"},
			wantErr: true,
		},
		{
			name:    "database url is required",
			env:     map[string]string{},
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
