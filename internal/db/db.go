// Package db connects to PostgreSQL and keeps its schema up to date.
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// connectWait is how long Connect waits for a database that isn't up yet.
// On a fresh deploy the app's pods can start before Postgres; waiting a
// little beats crash-looping with ever longer back-off.
const connectWait = time.Minute

// Connect opens a connection pool and checks the database is reachable, so a
// wrong URL or a database that stays down fails at startup rather than on
// the first request.
func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	deadline := time.Now().Add(connectWait)
	for {
		err := pool.Ping(ctx)
		if err == nil {
			return pool, nil
		}
		if time.Now().After(deadline) {
			pool.Close()
			return nil, fmt.Errorf("ping database: %w", err)
		}
		select {
		case <-ctx.Done():
			pool.Close()
			return nil, fmt.Errorf("ping database: %w", err)
		case <-time.After(2 * time.Second):
		}
	}
}
