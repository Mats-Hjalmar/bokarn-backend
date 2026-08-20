package tenant

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Operator is the tenant row itself.
type Operator struct {
	ID       string
	Slug     string
	Name     string
	OrgNo    string
	Country  string
	Currency string
	Locale   string
	Timezone string
}

// Store reads the pinned operator's own record.
type Store struct {
	db *DB
}

// NewStore constructs a Store.
func NewStore(d *DB) *Store { return &Store{db: d} }

// Current returns the pinned operator. The query carries no filter: the policy
// on tenants is `id = current_tenant_id()`, so exactly one row is visible and
// a WHERE clause would only restate it less reliably.
func (s *Store) Current(ctx context.Context) (Operator, error) {
	var o Operator
	err := s.db.ReadTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`select id::text, slug, name, coalesce(org_no, ''),
			        country, currency, locale, timezone
			   from tenants`,
		).Scan(&o.ID, &o.Slug, &o.Name, &o.OrgNo,
			&o.Country, &o.Currency, &o.Locale, &o.Timezone)
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Operator{}, ErrNoTenant
		}
		return Operator{}, fmt.Errorf("query operator: %w", err)
	}
	return o, nil
}
