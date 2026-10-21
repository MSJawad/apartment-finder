package listing

import (
	"context"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MSJawad/apartment-finder/internal/enrich"
	"github.com/MSJawad/apartment-finder/internal/queue"
	"github.com/MSJawad/apartment-finder/internal/region"
)

// examplesLockKey identifies the advisory lock SyncExamples holds. The value
// is arbitrary; it only has to differ from other locks.
const examplesLockKey = 7_270_003

// SyncExamples makes the example listings match the region config and
// returns how many it queued for measuring:
//   - examples missing from the database are added
//   - examples whose rent, size or rooms changed are updated
//   - examples no longer in the config are deleted
//   - examples whose measuring failed, say because the geocoder was down,
//     are tried again
//
// Each example is keyed by its address. It runs whenever the worker starts,
// under a lock, so two workers starting at once don't add everything twice.
func SyncExamples(ctx context.Context, pool *pgxpool.Pool, examples []region.Example) (int, error) {
	queued := 0
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", examplesLockKey); err != nil {
			return fmt.Errorf("lock examples: %w", err)
		}
		type stored struct {
			id            int64
			warmRentCents int
			sizeM2, rooms float64
			status        string
		}
		existing := map[string]stored{}
		rows, err := tx.Query(ctx, `
			SELECT id, address, warm_rent_cents, size_m2, rooms, status FROM listings WHERE visitor_id IS NULL`)
		if err != nil {
			return fmt.Errorf("load examples: %w", err)
		}
		var address string
		var s stored
		if _, err := pgx.ForEachRow(rows, []any{&s.id, &address, &s.warmRentCents, &s.sizeM2, &s.rooms, &s.status}, func() error {
			existing[address] = s
			return nil
		}); err != nil {
			return fmt.Errorf("load examples: %w", err)
		}

		for _, e := range examples {
			cents := int(math.Round(e.WarmRentEUR * 100))
			var id int64
			old, ok := existing[e.Address]
			delete(existing, e.Address)
			switch {
			case !ok:
				if err := tx.QueryRow(ctx, `
					INSERT INTO listings (visitor_id, source, address, warm_rent_cents, size_m2, rooms)
					VALUES (NULL, 'example', $1, $2, $3, $4) RETURNING id`,
					e.Address, cents, e.SizeM2, e.Rooms).Scan(&id); err != nil {
					return fmt.Errorf("add example %q: %w", e.Address, err)
				}
			case old.warmRentCents != cents || old.sizeM2 != e.SizeM2 || old.rooms != e.Rooms:
				id = old.id
				if _, err := tx.Exec(ctx, `
					UPDATE listings SET warm_rent_cents = $2, size_m2 = $3, rooms = $4, status = 'pending'
					WHERE id = $1`, id, cents, e.SizeM2, e.Rooms); err != nil {
					return fmt.Errorf("update example %q: %w", e.Address, err)
				}
			case old.status == "failed":
				id = old.id
			default:
				continue
			}
			if err := queue.Enqueue(ctx, tx, enrich.JobKind, enrich.Payload{ListingID: id}); err != nil {
				return err
			}
			queued++
		}

		for address, s := range existing {
			if _, err := tx.Exec(ctx, "DELETE FROM listings WHERE id = $1", s.id); err != nil {
				return fmt.Errorf("delete example %q: %w", address, err)
			}
		}
		return nil
	})
	return queued, err
}
