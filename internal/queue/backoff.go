package queue

import (
	"math/rand/v2"
	"time"
)

const (
	backoffBase = 30 * time.Second
	backoffMax  = time.Hour
)

// Backoff is how long to wait before retrying a job that failed on its
// attempt-th try: 30s, 1m, 2m, 4m and so on, doubling up to an hour. With the
// default 8 attempts, a job keeps retrying for about an hour, enough to ride
// out a typical upstream outage.
//
// The wait is randomly shortened by up to half, so jobs that failed together,
// say during an outage, don't all retry at the same moment.
func Backoff(attempt int) time.Duration {
	return backoff(attempt, func(n time.Duration) time.Duration {
		return rand.N(n) //nolint:gosec // jitter doesn't need cryptographic randomness
	})
}

// backoff is Backoff with the random source as a parameter, for tests.
// random(n) must return a value in [0, n).
func backoff(attempt int, random func(time.Duration) time.Duration) time.Duration {
	wait := backoffBase
	for i := 1; i < attempt && wait < backoffMax; i++ {
		wait *= 2
	}
	wait = min(wait, backoffMax)
	return wait - random(wait/2)
}
