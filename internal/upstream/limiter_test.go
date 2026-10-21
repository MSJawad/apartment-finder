package upstream

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestLimiterSpacesCalls(t *testing.T) {
	const interval = 50 * time.Millisecond
	l := NewLimiter(interval)

	// Four callers at once: the first goes immediately, the rest one
	// interval apart, so the last starts after three intervals.
	start := time.Now()
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if err := l.Wait(t.Context()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if elapsed := time.Since(start); elapsed < 3*interval {
		t.Errorf("4 calls took %v, want at least %v", elapsed, 3*interval)
	}
}

func TestLimiterWaitIsCancellable(t *testing.T) {
	l := NewLimiter(time.Hour)
	if err := l.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if err := l.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Wait() = %v, want DeadlineExceeded", err)
	}
}
