// Package config reads runtime settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
)

// Config holds the settings shared by the api and worker binaries.
type Config struct {
	// Addr is where the api serves public traffic.
	Addr string
	// AdminAddr serves /healthz, /readyz and /metrics. It is never exposed
	// through the ingress, so metrics stay private.
	AdminAddr string
	LogLevel  slog.Level
	// DatabaseURL is a PostgreSQL connection string. It holds a password, so
	// it comes from a Kubernetes Secret and never has a default.
	DatabaseURL string
	// RegionPath is the region config file, e.g. config/thuringia.json.
	RegionPath string
	// PhotonURL is the geocoder, komoot's public instance by default.
	PhotonURL string
	// OSRMWalkURL and OSRMBikeURL are the walking and cycling routers. Only
	// the worker uses them, so only the worker requires them; see
	// RequireRouting.
	OSRMWalkURL, OSRMBikeURL string
}

// Load builds a Config from getenv, falling back to defaults for unset
// variables. Taking getenv as a parameter keeps tests free of global state.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		Addr:        valueOr(getenv("ADDR"), ":8080"),
		AdminAddr:   valueOr(getenv("ADMIN_ADDR"), ":9090"),
		DatabaseURL: getenv("DATABASE_URL"),
		RegionPath:  getenv("REGION_CONFIG"),
		PhotonURL:   valueOr(getenv("PHOTON_URL"), "https://photon.komoot.io"),
		OSRMWalkURL: getenv("OSRM_WALK_URL"),
		OSRMBikeURL: getenv("OSRM_BIKE_URL"),
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	if cfg.RegionPath == "" {
		return Config{}, errors.New("REGION_CONFIG is required")
	}
	if raw := getenv("LOG_LEVEL"); raw != "" {
		if err := cfg.LogLevel.UnmarshalText([]byte(raw)); err != nil {
			return Config{}, fmt.Errorf("LOG_LEVEL: %w", err)
		}
	}
	return cfg, nil
}

// RequireRouting reports an error unless both routers are configured.
// There are no defaults: a wrong guess would only show up as failing jobs.
func (c Config) RequireRouting() error {
	if c.OSRMWalkURL == "" || c.OSRMBikeURL == "" {
		return errors.New("OSRM_WALK_URL and OSRM_BIKE_URL are required")
	}
	return nil
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
