package queue

import (
	"testing"
	"time"
)

func TestBackoff(t *testing.T) {
	noJitter := func(time.Duration) time.Duration { return 0 }
	maxJitter := func(n time.Duration) time.Duration { return n - 1 }

	tests := []struct {
		attempt int
		random  func(time.Duration) time.Duration
		want    time.Duration
	}{
		{attempt: 1, random: noJitter, want: 30 * time.Second},
		{attempt: 2, random: noJitter, want: time.Minute},
		{attempt: 3, random: noJitter, want: 2 * time.Minute},
		{attempt: 7, random: noJitter, want: 32 * time.Minute},
		{attempt: 8, random: noJitter, want: time.Hour}, // 64m, capped
		{attempt: 100, random: noJitter, want: time.Hour},
		{attempt: 0, random: noJitter, want: 30 * time.Second},
		{attempt: 1, random: maxJitter, want: 15*time.Second + 1},
		{attempt: 100, random: maxJitter, want: 30*time.Minute + 1},
	}
	for _, tt := range tests {
		if got := backoff(tt.attempt, tt.random); got != tt.want {
			t.Errorf("backoff(%d) = %v, want %v", tt.attempt, got, tt.want)
		}
	}
}

func TestBackoffStaysInRange(t *testing.T) {
	for attempt := 1; attempt <= 12; attempt++ {
		upper := backoff(attempt, func(time.Duration) time.Duration { return 0 })
		for range 100 {
			if got := Backoff(attempt); got <= upper/2 || got > upper {
				t.Fatalf("Backoff(%d) = %v, want in (%v, %v]", attempt, got, upper/2, upper)
			}
		}
	}
}
