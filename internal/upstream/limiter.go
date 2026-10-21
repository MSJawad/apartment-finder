package upstream

import (
	"context"
	"sync"
	"time"
)

// Limiter spaces calls at least an interval apart. It works within one
// process; the worker runs as a single replica, so that also holds for the
// whole deployment.
type Limiter struct {
	interval time.Duration
	mu       sync.Mutex
	next     time.Time // the earliest time the next call may start
}

// NewLimiter returns a Limiter that allows one call per interval.
func NewLimiter(interval time.Duration) *Limiter {
	return &Limiter{interval: interval}
}

// Wait blocks until the caller may make its call, or ctx is done.
func (l *Limiter) Wait(ctx context.Context) error {
	l.mu.Lock()
	start := time.Now()
	if l.next.After(start) {
		start = l.next
	}
	l.next = start.Add(l.interval)
	l.mu.Unlock()

	wait := time.Until(start)
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
