package db_test

import (
	"log/slog"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MSJawad/apartment-finder/internal/db"
	"github.com/MSJawad/apartment-finder/internal/dbtest"
)

var discard = slog.New(slog.DiscardHandler)

func TestMigrateCreatesSchemaOnce(t *testing.T) {
	t.Parallel()
	pool := dbtest.Empty(t)

	for range 2 {
		if err := db.Migrate(t.Context(), pool, discard); err != nil {
			t.Fatalf("Migrate() error = %v", err)
		}
	}

	var postgisVersion string
	if err := pool.QueryRow(t.Context(), "SELECT postgis_lib_version()").Scan(&postgisVersion); err != nil {
		t.Fatalf("PostGIS not installed: %v", err)
	}
	for _, table := range []string{"visitors", "listings", "jobs"} {
		if !tableExists(t, pool, table) {
			t.Errorf("table %s missing after Migrate", table)
		}
	}
	if n := count(t, pool, "SELECT count(*) FROM schema_migrations"); n != 2 {
		t.Errorf("schema_migrations has %d rows, want 2", n)
	}
}

func TestMigrateIsSafeToRunConcurrently(t *testing.T) {
	t.Parallel()
	pool := dbtest.Empty(t)

	// Like several pods starting at once: the advisory lock must make all but
	// one wait, and the waiters then find nothing left to do.
	var wg sync.WaitGroup
	errs := make([]error, 5)
	for i := range errs {
		wg.Go(func() { errs[i] = db.Migrate(t.Context(), pool, discard) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("Migrate() #%d error = %v", i, err)
		}
	}
	if n := count(t, pool, "SELECT count(*) FROM schema_migrations"); n != 2 {
		t.Errorf("schema_migrations has %d rows, want 2", n)
	}
}

func TestMigrateRollsBackAFailedMigration(t *testing.T) {
	t.Parallel()
	pool := dbtest.Empty(t)
	broken := fstest.MapFS{
		"migrations/0001_create_a.sql": {Data: []byte("CREATE TABLE a (id int)")},
		"migrations/0002_create_b.sql": {Data: []byte("CREATE TABLE b (id int); SELECT no_such_function()")},
	}

	err := db.MigrateFS(t.Context(), pool, broken, discard)
	if err == nil || !strings.Contains(err.Error(), "apply migration 0002_create_b") {
		t.Fatalf("Migrate() error = %v, want failure in 0002_create_b", err)
	}
	if !tableExists(t, pool, "a") {
		t.Error("table a missing: 0001 should have been applied")
	}
	if tableExists(t, pool, "b") {
		t.Error("table b exists: the failed migration was not rolled back")
	}

	fixed := fstest.MapFS{
		"migrations/0001_create_a.sql": broken["migrations/0001_create_a.sql"],
		"migrations/0002_create_b.sql": {Data: []byte("CREATE TABLE b (id int)")},
	}
	if err := db.MigrateFS(t.Context(), pool, fixed, discard); err != nil {
		t.Fatalf("Migrate() after fix error = %v", err)
	}
	if !tableExists(t, pool, "b") {
		t.Error("table b missing after the fixed migration ran")
	}
}

func TestMigrateRejectsAnEditedMigration(t *testing.T) {
	t.Parallel()
	pool := dbtest.Empty(t)
	original := fstest.MapFS{"migrations/0001_create_a.sql": {Data: []byte("CREATE TABLE a (id int)")}}
	edited := fstest.MapFS{"migrations/0001_create_a.sql": {Data: []byte("CREATE TABLE a (id bigint)")}}

	if err := db.MigrateFS(t.Context(), pool, original, discard); err != nil {
		t.Fatal(err)
	}
	err := db.MigrateFS(t.Context(), pool, edited, discard)
	if err == nil || !strings.Contains(err.Error(), "changed after it was applied") {
		t.Fatalf("Migrate() error = %v, want an edited-migration error", err)
	}
}

func tableExists(t *testing.T, pool *pgxpool.Pool, name string) bool {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(t.Context(), "SELECT to_regclass($1) IS NOT NULL", name).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

func count(t *testing.T, pool *pgxpool.Pool, sql string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), sql).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
