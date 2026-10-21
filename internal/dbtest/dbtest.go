// Package dbtest gives integration tests a real PostGIS database.
//
// The first call starts one container, built from deploy/postgis/Dockerfile
// so tests run against the same image as production, and prepares a template
// database with every migration applied. Each test then gets its own copy of
// that template. Copying a template is fast, and separate databases let tests
// run in parallel without seeing each other's rows. testcontainers removes
// the container when the test binary exits.
//
// These tests need Docker. Run go test -short to skip them.
package dbtest

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/MSJawad/apartment-finder/internal/db"
)

const migratedTemplate = "migrated_template"

var (
	startOnce sync.Once
	startErr  error
	// adminConfig connects to the container's default database, from which
	// test databases are created.
	adminConfig *pgxpool.Config

	// createMu serializes CREATE DATABASE, which fails if another session is
	// copying the same template at that moment.
	createMu sync.Mutex
	created  int
)

// New returns a pool connected to a new database with every migration
// applied.
func New(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return newDatabase(t, migratedTemplate)
}

// Empty returns a pool connected to a new database without any tables, for
// testing the migrations themselves.
func Empty(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return newDatabase(t, "template0")
}

func newDatabase(t *testing.T, template string) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test: needs Docker, skipped with -short")
	}
	startOnce.Do(start)
	if startErr != nil {
		t.Fatalf("start PostGIS container: %v", startErr)
	}

	createMu.Lock()
	created++
	name := fmt.Sprintf("test_%d", created)
	err := createDatabase(t.Context(), name, template)
	createMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	cfg := adminConfig.Copy()
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatalf("connect to %s: %v", name, err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func start() {
	ctx := context.Background()
	_, thisFile, _, _ := runtime.Caller(0)
	dockerDir := filepath.Join(filepath.Dir(thisFile), "..", "..", "deploy", "postgis")

	// The image is kept so the next run's build is a cache hit, under a fixed
	// name so runs don't each leave a new tag behind.
	ctr, err := postgres.Run(ctx, "",
		testcontainers.WithDockerfile(testcontainers.FromDockerfile{
			Context: dockerDir, Repo: "apartment-finder-postgis", Tag: "local", KeepImage: true,
		}),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		startErr = err
		return
	}
	url, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		startErr = err
		return
	}
	if adminConfig, err = pgxpool.ParseConfig(url); err != nil {
		startErr = err
		return
	}

	if err := createDatabase(ctx, migratedTemplate, "template0"); err != nil {
		startErr = err
		return
	}
	cfg := adminConfig.Copy()
	cfg.ConnConfig.Database = migratedTemplate
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		startErr = err
		return
	}
	// The template must have no open connections when it is copied.
	defer pool.Close()
	startErr = db.Migrate(ctx, pool, slog.New(slog.DiscardHandler))
}

func createDatabase(ctx context.Context, name, template string) error {
	conn, err := pgx.ConnectConfig(ctx, adminConfig.ConnConfig)
	if err != nil {
		return fmt.Errorf("connect to admin database: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	sql := fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s",
		pgx.Identifier{name}.Sanitize(), pgx.Identifier{template}.Sanitize())
	if _, err := conn.Exec(ctx, sql); err != nil {
		return fmt.Errorf("create database %s: %w", name, err)
	}
	return nil
}
