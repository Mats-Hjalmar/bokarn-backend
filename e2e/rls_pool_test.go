package e2e

import (
	"fmt"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestConcurrentTenantsNeverSeeEachOther is the regression test for the failure
// mode that makes this whole design fragile: a tenant pinned at session scope
// instead of transaction scope.
//
// With set_config(..., true) the value dies at COMMIT and the next borrower of
// the connection starts clean. With is_local = false — or with a pgxpool
// AfterConnect hook, which fires once per physical connection rather than once
// per request — the value outlives the request and whoever borrows that
// connection next reads someone else's data.
//
// The pool is deliberately tiny so every goroutine is forced to share
// connections; with a large pool the bug can hide behind spare capacity.
func TestConcurrentTenantsNeverSeeEachOther(t *testing.T) {
	pool := appPool(t, 2)

	const goroutines = 200

	want := map[string]string{storsand: "storsand", hamnviken: "hamnviken"}

	var wg sync.WaitGroup
	errs := make(chan error, goroutines)

	for i := range goroutines {
		tenant := storsand
		if i%2 == 1 {
			tenant = hamnviken
		}

		wg.Add(1)
		go func(tenant string) {
			defer wg.Done()

			tx, err := pool.Begin(t.Context())
			if err != nil {
				errs <- fmt.Errorf("begin: %w", err)
				return
			}
			defer func() { _ = tx.Rollback(t.Context()) }()

			var applied string
			if err := tx.QueryRow(t.Context(),
				`select set_config('app.tenant_id', $1, true)`, tenant,
			).Scan(&applied); err != nil {
				errs <- fmt.Errorf("pin: %w", err)
				return
			}

			var slug string
			var rows int
			if err := tx.QueryRow(t.Context(),
				`select (select slug from tenants),
				        (select count(*) from tenants)`,
			).Scan(&slug, &rows); err != nil {
				errs <- fmt.Errorf("read: %w", err)
				return
			}

			if rows != 1 {
				errs <- fmt.Errorf(
					"tenant %s saw %d operator rows, want exactly 1",
					want[tenant], rows)
				return
			}
			if slug != want[tenant] {
				errs <- fmt.Errorf(
					"tenant %s saw operator %q — a connection leaked its pin",
					want[tenant], slug)
			}
		}(tenant)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}
}

// A transaction that never pins must see nothing even while other transactions
// on the same small pool are pinned. This is the same property from the other
// side: not "does a pin leak out", but "does an unpinned reader pick one up".
func TestUnpinnedReaderNeverInheritsAPin(t *testing.T) {
	pool := appPool(t, 2)

	const rounds = 100

	var wg sync.WaitGroup
	errs := make(chan error, rounds*2)

	for range rounds {
		wg.Add(2)

		go func() {
			defer wg.Done()
			err := pool.AcquireFunc(t.Context(), func(c *pgxpool.Conn) error {
				tx, err := c.Begin(t.Context())
				if err != nil {
					return err
				}
				defer func() { _ = tx.Rollback(t.Context()) }()
				var applied string
				return tx.QueryRow(t.Context(),
					`select set_config('app.tenant_id', $1, true)`, storsand,
				).Scan(&applied)
			})
			if err != nil {
				errs <- err
			}
		}()

		go func() {
			defer wg.Done()
			var n int
			if err := pool.QueryRow(t.Context(),
				`select count(*) from tenants`).Scan(&n); err != nil {
				errs <- err
				return
			}
			if n != 0 {
				errs <- fmt.Errorf(
					"unpinned reader saw %d operator rows: a pin outlived its "+
						"transaction", n)
			}
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}
}
