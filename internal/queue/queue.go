// Package queue is a background job queue stored in PostgreSQL.
//
// Jobs are enqueued inside the caller's transaction, so a job exists exactly
// when the write that needs it commits: no listing without its geocoding job,
// and no job for a listing that was rolled back.
//
// Workers claim one job at a time with FOR UPDATE SKIP LOCKED, which lets
// many workers poll the same table without blocking on each other's rows.
// A claimed job is held by a time-limited lease, not an open transaction:
// jobs call slow external APIs, and a transaction held that long would tie up
// a connection and hold back Postgres' cleanup (vacuum). If a worker dies,
// its lease runs out and another worker retries the job.
//
// Delivery is at least once: a job whose lease runs out mid-run can run
// twice, so handlers must be safe to repeat.
package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	// ErrNoJob means no job is ready to run right now.
	ErrNoJob = errors.New("queue: no job ready")
	// ErrLeaseLost means the job's lease ran out and another worker claimed
	// it, so this worker's result was discarded.
	ErrLeaseLost = errors.New("queue: lease lost to another worker")
)

// DB is what Enqueue needs from a connection; both pgx.Tx and *pgxpool.Pool
// provide it.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Job is a claimed job.
type Job struct {
	ID          int64
	Kind        string
	Payload     json.RawMessage
	Attempts    int // including the current one
	MaxAttempts int
	leaseToken  string
}

// Enqueue adds a job that runs payload, encoded as JSON, through the handler
// for kind. Pass the transaction of the write that needs the job.
func Enqueue(ctx context.Context, db DB, kind string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode %s payload: %w", kind, err)
	}
	if _, err := db.Exec(ctx, "INSERT INTO jobs (kind, payload) VALUES ($1, $2)", kind, encoded); err != nil {
		return fmt.Errorf("enqueue %s: %w", kind, err)
	}
	return nil
}

// Queue claims jobs and records their results.
type Queue struct {
	pool  *pgxpool.Pool
	lease time.Duration
}

// New returns a Queue whose claims last for lease. The lease must be longer
// than any job takes, or a slow job is taken over while still running.
func New(pool *pgxpool.Pool, lease time.Duration) *Queue {
	return &Queue{pool: pool, lease: lease}
}

// Claim takes the oldest job that is due, or whose previous worker's lease
// ran out, and leases it to the caller. It returns ErrNoJob if there is none.
func (q *Queue) Claim(ctx context.Context) (Job, error) {
	for {
		var job Job
		err := q.pool.QueryRow(ctx, `
			UPDATE jobs
			SET status = 'running',
			    attempts = attempts + 1,
			    locked_until = now() + make_interval(secs => $1),
			    lease_token = gen_random_uuid(),
			    updated_at = now()
			WHERE id = (
			    SELECT id FROM jobs
			    WHERE (status = 'pending' AND run_at <= now())
			       OR (status = 'running' AND locked_until <= now())
			    ORDER BY run_at, id
			    LIMIT 1
			    FOR UPDATE SKIP LOCKED
			)
			RETURNING id, kind, payload, attempts, max_attempts, lease_token::text`,
			q.lease.Seconds(),
		).Scan(&job.ID, &job.Kind, &job.Payload, &job.Attempts, &job.MaxAttempts, &job.leaseToken)
		if errors.Is(err, pgx.ErrNoRows) {
			return Job{}, ErrNoJob
		}
		if err != nil {
			return Job{}, fmt.Errorf("claim job: %w", err)
		}
		if job.Attempts <= job.MaxAttempts {
			return job, nil
		}
		// Every attempt so far ended with the lease running out, most likely
		// because the job crashes its worker. Stop retrying it.
		if _, err := q.Fail(ctx, job, errors.New("lease expired on every attempt"), 0); err != nil {
			return Job{}, err
		}
	}
}

// Complete marks job as done.
func (q *Queue) Complete(ctx context.Context, job Job) error {
	tag, err := q.pool.Exec(ctx, `
		UPDATE jobs
		SET status = 'done', locked_until = NULL, lease_token = NULL, last_error = NULL, updated_at = now()
		WHERE id = $1 AND status = 'running' AND lease_token = $2::uuid`,
		job.ID, job.leaseToken)
	if err != nil {
		return fmt.Errorf("complete job %d: %w", job.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLeaseLost
	}
	return nil
}

// Fail records that job failed with cause. If it has attempts left, it runs
// again after retryIn; otherwise it is marked dead, and Fail reports that.
func (q *Queue) Fail(ctx context.Context, job Job, cause error, retryIn time.Duration) (dead bool, err error) {
	var status string
	err = q.pool.QueryRow(ctx, `
		UPDATE jobs
		SET status = CASE WHEN attempts >= max_attempts THEN 'dead' ELSE 'pending' END,
		    run_at = now() + make_interval(secs => $3),
		    locked_until = NULL,
		    lease_token = NULL,
		    last_error = $4,
		    updated_at = now()
		WHERE id = $1 AND status = 'running' AND lease_token = $2::uuid
		RETURNING status`,
		job.ID, job.leaseToken, retryIn.Seconds(), cause.Error(),
	).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrLeaseLost
	}
	if err != nil {
		return false, fmt.Errorf("fail job %d: %w", job.ID, err)
	}
	return status == "dead", nil
}

// DeleteFinished deletes jobs that finished a while ago: done jobs after
// keepDone, and dead ones after keepDead, which is longer so failures stay
// around to be looked into. It returns how many it deleted.
func DeleteFinished(ctx context.Context, db DB, keepDone, keepDead time.Duration) (int64, error) {
	tag, err := db.Exec(ctx, `
		DELETE FROM jobs
		WHERE (status = 'done' AND updated_at < now() - make_interval(secs => $1))
		   OR (status = 'dead' AND updated_at < now() - make_interval(secs => $2))`,
		keepDone.Seconds(), keepDead.Seconds())
	if err != nil {
		return 0, fmt.Errorf("delete finished jobs: %w", err)
	}
	return tag.RowsAffected(), nil
}
