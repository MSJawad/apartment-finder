package visitor

import (
	"context"
	"fmt"
	"math"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
)

// submissionLockKey identifies the advisory lock Reserve holds while
// counting. The value is arbitrary; it only has to differ from other locks.
const submissionLockKey = 7_270_002

// Limits caps how many listings and places can be added. Each addition costs
// a geocoding request to a free public service plus routing work, so the
// caps protect that service's goodwill and our small server.
//
// Windows are rolling: "per day" means the last 24 hours, which needs no
// time zone and has no midnight rush.
type Limits struct {
	PerHour, PerDay int // per visitor
	// SiteWidePerDay caps everyone together. Clearing cookies gets around
	// the per-visitor limits, but not this one.
	SiteWidePerDay int
}

// LimitError means an addition was refused because a limit was reached.
type LimitError struct {
	Reason string
	// RetryAfter is when the oldest counted addition leaves its window,
	// freeing a slot.
	RetryAfter time.Duration
}

func (e *LimitError) Error() string { return e.Reason }

// Reserve records one addition by visitor, or returns a *LimitError if that
// would exceed a limit. Call it in the transaction that inserts the listing
// or place, so a rolled-back insert doesn't use up a slot.
//
// It takes a transaction-scoped lock first, so concurrent additions are
// counted one at a time: without it, two requests could both see 9 of 10
// used and both go through. One lock for everyone is fine at a few hundred
// additions a day.
func (l Limits) Reserve(ctx context.Context, tx pgx.Tx, visitor uuid.UUID) error {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", submissionLockKey); err != nil {
		return fmt.Errorf("lock submissions: %w", err)
	}
	// The retry times are worked out in SQL so they use the database clock,
	// the same one that stamped the rows.
	var hour, day, siteWide int
	var hourRetry, dayRetry, siteWideRetry float64
	err := tx.QueryRow(ctx, `
		SELECT
		    count(*) FILTER (WHERE visitor_id = $1 AND created_at > now() - interval '1 hour'),
		    count(*) FILTER (WHERE visitor_id = $1),
		    count(*),
		    coalesce(extract(epoch FROM min(created_at) FILTER (
		        WHERE visitor_id = $1 AND created_at > now() - interval '1 hour')
		        + interval '1 hour' - now()), 0),
		    coalesce(extract(epoch FROM min(created_at) FILTER (WHERE visitor_id = $1)
		        + interval '1 day' - now()), 0),
		    coalesce(extract(epoch FROM min(created_at) + interval '1 day' - now()), 0)
		FROM submissions
		WHERE created_at > now() - interval '1 day'`, visitor,
	).Scan(&hour, &day, &siteWide, &hourRetry, &dayRetry, &siteWideRetry)
	if err != nil {
		return fmt.Errorf("count submissions: %w", err)
	}

	switch {
	case siteWide >= l.SiteWidePerDay:
		return &LimitError{"The site has reached its limit for today. Please try again later.", seconds(siteWideRetry)}
	case day >= l.PerDay:
		return &LimitError{fmt.Sprintf("You can add %d a day. Please try again later.", l.PerDay), seconds(dayRetry)}
	case hour >= l.PerHour:
		return &LimitError{fmt.Sprintf("You can add %d an hour. Please try again later.", l.PerHour), seconds(hourRetry)}
	}
	if _, err := tx.Exec(ctx, "INSERT INTO submissions (visitor_id) VALUES ($1)", visitor); err != nil {
		return fmt.Errorf("record submission: %w", err)
	}
	return nil
}

// DeleteOldSubmissions deletes submissions that no longer count towards any
// limit.
func DeleteOldSubmissions(ctx context.Context, db DB) error {
	if _, err := db.Exec(ctx, "DELETE FROM submissions WHERE created_at < now() - interval '1 day'"); err != nil {
		return fmt.Errorf("delete old submissions: %w", err)
	}
	return nil
}

// seconds rounds up to whole seconds, which is what the Retry-After header
// holds, so a client that waits that long is never early.
func seconds(s float64) time.Duration {
	return time.Duration(math.Ceil(max(s, 1))) * time.Second
}
