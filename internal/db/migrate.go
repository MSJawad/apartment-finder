package db

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// migrationLockKey identifies the advisory lock held while migrating. The
// value is arbitrary; it only has to be the same in every process.
const migrationLockKey = 7_270_001

// migrationName is the required file name shape, e.g. 0002_create_jobs.sql.
// The number prefix fixes the order migrations run in.
var migrationName = regexp.MustCompile(`^[0-9]{4}_[a-z0-9_]+\.sql$`)

type migration struct {
	version  string // the file name without .sql
	sql      string
	checksum string
}

// Migrate applies, in order, every embedded migration the database hasn't
// recorded yet.
func Migrate(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) error {
	return migrate(ctx, pool, migrationFiles, logger)
}

// migrate is Migrate with the migration files as a parameter, for tests.
//
// It holds a Postgres advisory lock for the whole run: the api and worker both
// migrate on startup, and when several pods start at once, all but one wait
// here instead of applying the same migration twice. Each migration runs in
// its own transaction together with the row recording it, so a failed
// migration leaves no trace and is retried on the next start.
func migrate(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS, logger *slog.Logger) error {
	migrations, err := loadMigrations(fsys)
	if err != nil {
		return err
	}

	// An advisory lock belongs to the connection that took it, so every
	// statement below must use this one connection, not the pool.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockKey); err != nil {
		return fmt.Errorf("take migration lock: %w", err)
	}
	defer func() {
		cleanupCtx := context.WithoutCancel(ctx)
		if _, err := conn.Exec(cleanupCtx, "SELECT pg_advisory_unlock($1)", migrationLockKey); err != nil {
			// The pool would hand this connection out again with the lock
			// still held, blocking every later migration. Closing it drops
			// the lock, and the pool discards closed connections.
			_ = conn.Conn().Close(cleanupCtx)
		}
	}()

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    text PRIMARY KEY,
			checksum   text NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	applied, err := appliedChecksums(ctx, conn.Conn())
	if err != nil {
		return err
	}

	for _, m := range migrations {
		if checksum, ok := applied[m.version]; ok {
			// Editing a migration that already ran would leave databases that
			// ran the old version silently different from new ones.
			if checksum != m.checksum {
				return fmt.Errorf("migration %s changed after it was applied; add a new migration instead", m.version)
			}
			continue
		}
		err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			// Exec without arguments uses Postgres' simple protocol, which
			// allows several statements in one call.
			if _, err := tx.Exec(ctx, m.sql); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version, checksum) VALUES ($1, $2)", m.version, m.checksum)
			return err
		})
		if err != nil {
			return fmt.Errorf("apply migration %s: %w", m.version, err)
		}
		logger.LogAttrs(ctx, slog.LevelInfo, "applied migration", slog.String("version", m.version))
	}
	return nil
}

// loadMigrations reads every .sql file in the migrations directory of fsys,
// sorted by name.
func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	migrations := make([]migration, 0, len(entries))
	for _, entry := range entries { // fs.ReadDir returns entries sorted by name
		name := entry.Name()
		if !migrationName.MatchString(name) {
			return nil, fmt.Errorf("migration file %q: name must look like 0001_create_things.sql", name)
		}
		content, err := fs.ReadFile(fsys, path.Join("migrations", name))
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", name, err)
		}
		sum := sha256.Sum256(content)
		migrations = append(migrations, migration{
			version:  strings.TrimSuffix(name, ".sql"),
			sql:      string(content),
			checksum: hex.EncodeToString(sum[:]),
		})
	}
	for i := 1; i < len(migrations); i++ {
		if migrations[i].version[:4] == migrations[i-1].version[:4] {
			return nil, fmt.Errorf("migrations %s and %s share a number", migrations[i-1].version, migrations[i].version)
		}
	}
	return migrations, nil
}

func appliedChecksums(ctx context.Context, conn *pgx.Conn) (map[string]string, error) {
	rows, err := conn.Query(ctx, "SELECT version, checksum FROM schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("read applied migrations: %w", err)
	}
	applied := map[string]string{}
	var version, checksum string
	_, err = pgx.ForEachRow(rows, []any{&version, &checksum}, func() error {
		applied[version] = checksum
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read applied migrations: %w", err)
	}
	return applied, nil
}
