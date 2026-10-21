package queue_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/MSJawad/apartment-finder/internal/dbtest"
	"github.com/MSJawad/apartment-finder/internal/queue"
)

func TestEnqueueCommitsAndRollsBackWithTheCallersTransaction(t *testing.T) {
	t.Parallel()
	pool := dbtest.New(t)

	for _, commit := range []bool{false, true} {
		err := pgx.BeginFunc(t.Context(), pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(t.Context(), `
				INSERT INTO listings (address, warm_rent_cents, size_m2, rooms)
				VALUES ('Anger 1, Erfurt', 85000, 62, 2.5)`); err != nil {
				return err
			}
			if err := queue.Enqueue(t.Context(), tx, "geocode", map[string]int{"listing_id": 1}); err != nil {
				return err
			}
			if !commit {
				return errors.New("roll back")
			}
			return nil
		})
		if commit && err != nil {
			t.Fatal(err)
		}
		wantJobs := 0
		if commit {
			wantJobs = 1
		}
		if n := count(t, pool, "SELECT count(*) FROM jobs"); n != wantJobs {
			t.Errorf("commit=%v: %d jobs, want %d", commit, n, wantJobs)
		}
	}
}

func TestConcurrentWorkersRunEveryJobExactlyOnce(t *testing.T) {
	t.Parallel()
	pool := dbtest.New(t)
	const jobs = 200
	for range jobs {
		if err := queue.Enqueue(t.Context(), pool, "noop", nil); err != nil {
			t.Fatal(err)
		}
	}

	var mu sync.Mutex
	runs := map[int64]int{}
	runner := newRunner(pool, time.Minute, map[string]queue.Handler{
		"noop": func(_ context.Context, job queue.Job) error {
			mu.Lock()
			runs[job.ID]++
			mu.Unlock()
			return nil
		},
	})
	runner.Workers = 5

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { runner.Run(ctx); close(done) }()
	waitFor(t, func() bool { return count(t, pool, "SELECT count(*) FROM jobs WHERE status = 'done'") == jobs })
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(runs) != jobs {
		t.Errorf("%d distinct jobs ran, want %d", len(runs), jobs)
	}
	for id, n := range runs {
		if n != 1 {
			t.Errorf("job %d ran %d times, want 1", id, n)
		}
	}
}

func TestExpiredLeaseLetsAnotherWorkerTakeOver(t *testing.T) {
	t.Parallel()
	pool := dbtest.New(t)
	q := queue.New(pool, 100*time.Millisecond)
	if err := queue.Enqueue(t.Context(), pool, "noop", nil); err != nil {
		t.Fatal(err)
	}

	crashed, err := q.Claim(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Claim(t.Context()); !errors.Is(err, queue.ErrNoJob) {
		t.Fatalf("Claim() during the lease = %v, want ErrNoJob", err)
	}

	time.Sleep(200 * time.Millisecond) // the first worker "crashed"; its lease runs out
	takeover, err := q.Claim(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if takeover.ID != crashed.ID || takeover.Attempts != 2 {
		t.Fatalf("took over job %d attempt %d, want job %d attempt 2", takeover.ID, takeover.Attempts, crashed.ID)
	}

	// The first worker comes back to life; its result must not count.
	if err := q.Complete(t.Context(), crashed); !errors.Is(err, queue.ErrLeaseLost) {
		t.Errorf("Complete() with the old lease = %v, want ErrLeaseLost", err)
	}
	if err := q.Complete(t.Context(), takeover); err != nil {
		t.Errorf("Complete() with the current lease = %v", err)
	}
}

func TestFailingJobRetriesUntilDead(t *testing.T) {
	t.Parallel()
	pool := dbtest.New(t)
	q := queue.New(pool, time.Minute)
	if err := queue.Enqueue(t.Context(), pool, "flaky", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), "UPDATE jobs SET max_attempts = 3"); err != nil {
		t.Fatal(err)
	}

	for attempt := 1; attempt <= 3; attempt++ {
		job, err := q.Claim(t.Context())
		if err != nil {
			t.Fatalf("attempt %d: Claim() = %v", attempt, err)
		}
		dead, err := q.Fail(t.Context(), job, errors.New("upstream returned 503"), 0)
		if err != nil {
			t.Fatal(err)
		}
		if wantDead := attempt == 3; dead != wantDead {
			t.Fatalf("attempt %d: dead = %v, want %v", attempt, dead, wantDead)
		}
	}
	if _, err := q.Claim(t.Context()); !errors.Is(err, queue.ErrNoJob) {
		t.Errorf("Claim() after death = %v, want ErrNoJob", err)
	}
	var status, lastError string
	if err := pool.QueryRow(t.Context(), "SELECT status, last_error FROM jobs").Scan(&status, &lastError); err != nil {
		t.Fatal(err)
	}
	if status != "dead" || lastError != "upstream returned 503" {
		t.Errorf("job = %s (%q), want dead with the last error", status, lastError)
	}
}

func TestRetryWaitsForTheBackoff(t *testing.T) {
	t.Parallel()
	pool := dbtest.New(t)
	if err := queue.Enqueue(t.Context(), pool, "flaky", nil); err != nil {
		t.Fatal(err)
	}
	runner := newRunner(pool, time.Minute, map[string]queue.Handler{
		"flaky": func(context.Context, queue.Job) error { return errors.New("timeout") },
	})

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { runner.Run(ctx); close(done) }()
	waitFor(t, func() bool {
		return count(t, pool, "SELECT count(*) FROM jobs WHERE status = 'pending' AND attempts = 1") == 1
	})
	cancel()
	<-done

	// The first retry waits 15 to 30 seconds (30s minus jitter).
	var waitSeconds float64
	if err := pool.QueryRow(t.Context(),
		"SELECT extract(epoch FROM run_at - now()) FROM jobs").Scan(&waitSeconds); err != nil {
		t.Fatal(err)
	}
	if waitSeconds < 14 || waitSeconds > 30 {
		t.Errorf("retry scheduled in %.1fs, want 15-30s", waitSeconds)
	}
}

func TestJobThatKeepsCrashingItsWorkerGoesDead(t *testing.T) {
	t.Parallel()
	pool := dbtest.New(t)
	q := queue.New(pool, 50*time.Millisecond)
	if err := queue.Enqueue(t.Context(), pool, "crashy", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), "UPDATE jobs SET max_attempts = 2"); err != nil {
		t.Fatal(err)
	}

	for range 2 { // claim, then "crash" without recording a result
		if _, err := q.Claim(t.Context()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, err := q.Claim(t.Context()); !errors.Is(err, queue.ErrNoJob) {
		t.Fatalf("Claim() after the last attempt = %v, want ErrNoJob", err)
	}
	var status string
	if err := pool.QueryRow(t.Context(), "SELECT status FROM jobs").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "dead" {
		t.Errorf("status = %s, want dead", status)
	}
}

func newRunner(pool *pgxpool.Pool, lease time.Duration, handlers map[string]queue.Handler) *queue.Runner {
	return &queue.Runner{
		Queue:        queue.New(pool, lease),
		Handlers:     handlers,
		Workers:      1,
		PollInterval: 10 * time.Millisecond,
		JobTimeout:   10 * time.Second,
		Logger:       slog.New(slog.DiscardHandler),
		Metrics:      queue.NewMetrics(prometheus.NewRegistry()),
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 30s")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func count(t *testing.T, pool *pgxpool.Pool, sql string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), sql).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
