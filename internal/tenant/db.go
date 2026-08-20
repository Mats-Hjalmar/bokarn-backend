package tenant

import (
	"context"
	"fmt"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DB is the only handle a domain package is given. It has no method that runs
// a query outside a tenant-pinned transaction, so a store physically cannot
// reach an unpinned connection.
type DB struct {
	pool *pgxpool.Pool
}

// New wraps a pool after asserting it is connected as a role that row-level
// security actually applies to. A superuser or BYPASSRLS role leaves every
// policy inert with no error anywhere, so this is checked once at boot and the
// process refuses to start rather than serving one operator's data to another.
func New(ctx context.Context, pool *pgxpool.Pool) (*DB, error) {
	if err := db.AssertRestrictedRole(ctx, pool); err != nil {
		return nil, err
	}
	return &DB{pool: pool}, nil
}

// Pool exposes the underlying pool for the few callers that legitimately work
// outside a tenant: the health probe and the per-tenant job dispatcher, which
// lists operators before running one transaction each.
func (d *DB) Pool() *pgxpool.Pool { return d.pool }

// Tx opens a transaction, pins ctx's tenant to it for its whole lifetime, and
// runs fn. It commits when fn returns nil and rolls back otherwise.
func (d *DB) Tx(ctx context.Context, fn func(pgx.Tx) error) error {
	return d.run(ctx, pgx.TxOptions{}, fn)
}

// ReadTx is Tx with a read-only transaction. Use it for every read path: the
// store then cannot write by accident, and the transaction can later be routed
// to a replica without revisiting callers.
func (d *DB) ReadTx(ctx context.Context, fn func(pgx.Tx) error) error {
	return d.run(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly}, fn)
}

func (d *DB) run(
	ctx context.Context,
	opts pgx.TxOptions,
	fn func(pgx.Tx) error,
) error {
	id, err := FromContext(ctx)
	if err != nil {
		return err
	}

	tx, err := d.pool.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := pin(ctx, tx, id); err != nil {
		return err
	}

	if err := fn(tx); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// pin sets the transaction-local GUC the policies read.
//
// set_config(..., true) rather than SET LOCAL: SET LOCAL cannot be
// parameterized, which would mean concatenating a session-derived id into SQL;
// it returns nothing, so the caller cannot verify it took; and outside a
// transaction it emits only a WARNING that pgx will not surface. The applied
// value is compared because a silently unpinned transaction reads as an empty
// database rather than as an error.
//
// is_local = true is what makes this safe under pooling: a session-scoped GUC
// would outlive the request and the pool hands the connection to whoever asks
// next.
func pin(ctx context.Context, tx pgx.Tx, id ID) error {
	var applied string
	err := tx.QueryRow(ctx,
		`select set_config('app.tenant_id', $1, true)`, string(id),
	).Scan(&applied)
	if err != nil {
		return fmt.Errorf("pin tenant: %w", err)
	}
	if applied != string(id) {
		return fmt.Errorf("pin tenant: applied %q, wanted %q", applied, id)
	}
	return nil
}
