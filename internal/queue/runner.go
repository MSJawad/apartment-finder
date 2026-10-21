package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// recordTimeout bounds writing a job's result back to the database.
const recordTimeout = 5 * time.Second

// Handler processes one job. Returning an error schedules a retry.
type Handler func(ctx context.Context, job Job) error

// Runner claims jobs and runs them through their kind's Handler.
type Runner struct {
	Queue    *Queue
	Handlers map[string]Handler
	// Workers is how many jobs run at once.
	Workers int
	// PollInterval is how long an idle worker waits before looking for a job
	// again.
	PollInterval time.Duration
	// JobTimeout bounds a single run. Keep it below the queue's lease, so a
	// slow job is cancelled before another worker can take it over.
	JobTimeout time.Duration
	Logger     *slog.Logger
	Metrics    *Metrics
}

// Run processes jobs until ctx is cancelled, then waits for the jobs in
// progress to finish.
func (r *Runner) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for range r.Workers {
		wg.Go(func() { r.work(ctx) })
	}
	wg.Wait()
}

func (r *Runner) work(ctx context.Context) {
	for ctx.Err() == nil {
		job, err := r.Queue.Claim(ctx)
		if err != nil {
			if !errors.Is(err, ErrNoJob) && ctx.Err() == nil {
				r.Logger.LogAttrs(ctx, slog.LevelError, "claim job", slog.Any("error", err))
			}
			select {
			case <-ctx.Done():
			case <-time.After(r.PollInterval):
			}
			continue
		}
		r.process(ctx, job)
	}
}

// process runs job and records the result.
func (r *Runner) process(ctx context.Context, job Job) {
	// Shutting down doesn't cancel a job in progress: it gets to finish, up
	// to JobTimeout, rather than being abandoned halfway and retried.
	jobCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.JobTimeout)
	defer cancel()

	start := time.Now()
	err := r.run(jobCtx, job)
	r.Metrics.duration.WithLabelValues(job.Kind).Observe(time.Since(start).Seconds())

	// Recording the result gets its own deadline: a job that ran into its
	// timeout has already used up jobCtx.
	recordCtx, cancelRecord := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
	defer cancelRecord()

	attrs := []slog.Attr{
		slog.Int64("job_id", job.ID), slog.String("kind", job.Kind), slog.Int("attempt", job.Attempts),
	}
	if err == nil {
		if err := r.Queue.Complete(recordCtx, job); err != nil {
			r.Logger.LogAttrs(ctx, slog.LevelError, "complete job", append(attrs, slog.Any("error", err))...)
			return
		}
		r.Metrics.jobs.WithLabelValues(job.Kind, "done").Inc()
		r.Logger.LogAttrs(ctx, slog.LevelInfo, "job done", attrs...)
		return
	}

	retryIn := Backoff(job.Attempts)
	dead, failErr := r.Queue.Fail(recordCtx, job, err, retryIn)
	if failErr != nil {
		r.Logger.LogAttrs(ctx, slog.LevelError, "record job failure",
			append(attrs, slog.Any("error", err), slog.Any("record_error", failErr))...)
		return
	}
	attrs = append(attrs, slog.Any("error", err))
	if dead {
		r.Metrics.jobs.WithLabelValues(job.Kind, "dead").Inc()
		r.Logger.LogAttrs(ctx, slog.LevelError, "job dead, no attempts left", attrs...)
		return
	}
	r.Metrics.jobs.WithLabelValues(job.Kind, "retry").Inc()
	r.Logger.LogAttrs(ctx, slog.LevelWarn, "job failed, will retry", append(attrs, slog.Duration("retry_in", retryIn))...)
}

// run calls the handler for job's kind, turning a panic into an error so one
// bad job can't crash the worker and every job running beside it.
func (r *Runner) run(ctx context.Context, job Job) (err error) {
	handler, ok := r.Handlers[job.Kind]
	if !ok {
		// Usually a newer api enqueued a kind this worker doesn't know yet;
		// retrying gives the rollout time to finish.
		return fmt.Errorf("no handler for job kind %q", job.Kind)
	}
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("handler panicked: %v", p)
		}
	}()
	return handler(ctx, job)
}

// Metrics are the job metrics recorded by Runner.
type Metrics struct {
	jobs     *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

// NewMetrics creates the job metrics and registers them with reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		jobs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jobs_processed_total",
			Help: "Jobs processed, by kind and outcome (done, retry or dead).",
		}, []string{"kind", "outcome"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "job_duration_seconds",
			Help:    "Time spent running jobs, by kind.",
			Buckets: prometheus.DefBuckets,
		}, []string{"kind"}),
	}
	reg.MustRegister(m.jobs, m.duration)
	return m
}
