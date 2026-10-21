package listing_test

import (
	"testing"

	"github.com/MSJawad/apartment-finder/internal/dbtest"
	"github.com/MSJawad/apartment-finder/internal/listing"
	"github.com/MSJawad/apartment-finder/internal/region"
)

func TestSyncExamples(t *testing.T) {
	t.Parallel()
	pool := dbtest.New(t)
	sync := func(examples ...region.Example) int {
		t.Helper()
		queued, err := listing.SyncExamples(t.Context(), pool, examples)
		if err != nil {
			t.Fatal(err)
		}
		return queued
	}
	johannes := region.Example{Address: "Johannesstraße, Erfurt", WarmRentEUR: 780, SizeM2: 58, Rooms: 2}
	krämpfer := region.Example{Address: "Krämpferstraße, Erfurt", WarmRentEUR: 960, SizeM2: 74, Rooms: 3}

	if n := sync(johannes, krämpfer); n != 2 {
		t.Errorf("first sync queued %d, want both", n)
	}
	if n := sync(johannes, krämpfer); n != 0 {
		t.Errorf("unchanged sync queued %d, want 0", n)
	}

	// A changed rent is updated and measured again; a removed example goes.
	johannes.WarmRentEUR = 800
	if n := sync(johannes); n != 1 {
		t.Errorf("sync after a change queued %d, want 1", n)
	}
	var address string
	var cents int
	var status, source string
	if err := pool.QueryRow(t.Context(), `
		SELECT address, warm_rent_cents, status, source FROM listings WHERE visitor_id IS NULL`,
	).Scan(&address, &cents, &status, &source); err != nil {
		t.Fatalf("want exactly one example left: %v", err)
	}
	if address != johannes.Address || cents != 80000 || status != "pending" || source != "example" {
		t.Errorf("example = %s, %d cents, %s, from %s", address, cents, status, source)
	}

	// One whose measuring failed is tried again at the next start.
	if _, err := pool.Exec(t.Context(), "UPDATE listings SET status = 'failed'"); err != nil {
		t.Fatal(err)
	}
	if n := sync(johannes); n != 1 {
		t.Errorf("sync with a failed example queued %d, want 1", n)
	}
	var jobs int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM jobs").Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 4 {
		t.Errorf("%d jobs, want 4: two added, one changed, one retried", jobs)
	}
}
