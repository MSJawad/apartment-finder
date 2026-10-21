package config

import (
	"log/slog"
	"testing"
)

func TestLoad(t *testing.T) {
	const dbURL, region = "postgres://app@db:5432/app", "config/thuringia.json"
	tests := []struct {
		name    string
		env     map[string]string
		want    Config
		wantErr bool
	}{
		{
			name: "defaults",
			env:  map[string]string{"DATABASE_URL": dbURL, "REGION_CONFIG": region},
			want: Config{Addr: ":8080", AdminAddr: ":9090", LogLevel: slog.LevelInfo, DatabaseURL: dbURL, RegionPath: region, PhotonURL: "https://photon.komoot.io"},
		},
		{
			name: "overrides",
			env: map[string]string{
				"DATABASE_URL": dbURL, "REGION_CONFIG": region, "ADDR": ":8000", "ADMIN_ADDR": "127.0.0.1:9000", "LOG_LEVEL": "debug",
				"PHOTON_URL": "http://photon:2322", "OSRM_WALK_URL": "http://osrm-walk:5000", "OSRM_BIKE_URL": "http://osrm-bike:5000",
			},
			want: Config{
				Addr: ":8000", AdminAddr: "127.0.0.1:9000", LogLevel: slog.LevelDebug, DatabaseURL: dbURL, RegionPath: region,
				PhotonURL: "http://photon:2322", OSRMWalkURL: "http://osrm-walk:5000", OSRMBikeURL: "http://osrm-bike:5000",
			},
		},
		{
			name: "log level is case insensitive",
			env:  map[string]string{"DATABASE_URL": dbURL, "REGION_CONFIG": region, "LOG_LEVEL": "WARN"},
			want: Config{Addr: ":8080", AdminAddr: ":9090", LogLevel: slog.LevelWarn, DatabaseURL: dbURL, RegionPath: region, PhotonURL: "https://photon.komoot.io"},
		},
		{
			name:    "unknown log level",
			env:     map[string]string{"DATABASE_URL": dbURL, "REGION_CONFIG": region, "LOG_LEVEL": "loud"},
			wantErr: true,
		},
		{
			name:    "database url is required",
			env:     map[string]string{"REGION_CONFIG": region},
			wantErr: true,
		},
		{
			name:    "region config is required",
			env:     map[string]string{"DATABASE_URL": dbURL},
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

func TestRequireRouting(t *testing.T) {
	for _, tt := range []struct {
		walk, bike string
		wantErr    bool
	}{
		{"http://osrm-walk:5000", "http://osrm-bike:5000", false},
		{"http://osrm-walk:5000", "", true},
		{"", "", true},
	} {
		err := Config{OSRMWalkURL: tt.walk, OSRMBikeURL: tt.bike}.RequireRouting()
		if (err != nil) != tt.wantErr {
			t.Errorf("RequireRouting() with %q and %q = %v, want error %v", tt.walk, tt.bike, err, tt.wantErr)
		}
	}
}
