// Package platform is the only place a connection that bypasses row-level
// security is allowed to exist. It serves bokarn's own operators, not the
// campsites', and every read it performs is recorded before it returns.
//
// It owns its pool rather than receiving one, so no domain package can be
// handed a privileged handle by accident, and the pool is deliberately small:
// this is an administrative path, not a request path.
package platform

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// maxConns caps the privileged pool. Cross-tenant work is rare and serialising
// it is preferable to leaving many bypass connections idle.
const maxConns = 4

// Tenant is an operator as the platform sees it.
type Tenant struct {
	ID      string `json:"id"`
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	Country string `json:"country"`
	Sites   int    `json:"sites"`
}

// Store performs audited cross-tenant reads.
type Store struct {
	pool *pgxpool.Pool
}

// New opens the privileged pool. It asserts the opposite of everything else in
// the codebase: this role is supposed to bypass row-level security, and a
// deployment that gave it a restricted DSN would fail silently by returning
// nothing rather than loudly.
func New(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse platform config: %w", err)
	}
	cfg.MaxConns = maxConns

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create platform pool: %w", err)
	}

	var role string
	var bypass bool
	err = pool.QueryRow(ctx,
		`select current_user, rolbypassrls
		   from pg_roles where rolname = current_user`,
	).Scan(&role, &bypass)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("read platform role: %w", err)
	}
	if !bypass {
		pool.Close()
		return nil, fmt.Errorf(
			"platform role %q does not bypass row-level security: "+
				"cross-tenant reads would silently return nothing", role,
		)
	}

	return &Store{pool: pool}, nil
}

// Close releases the pool.
func (s *Store) Close() { s.pool.Close() }

// ListTenants returns every operator, recording the read in the same
// transaction so a successful listing cannot go unlogged.
func (s *Store) ListTenants(
	ctx context.Context,
	actorExternalID string,
) ([]Tenant, error) {
	var out []Tenant

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin platform tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx,
		`select t.id::text, t.slug, t.name, t.country,
		        (select count(*) from sites s where s.tenant_id = t.id)
		   from tenants t order by t.slug`)
	if err != nil {
		return nil, fmt.Errorf("query tenants: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var t Tenant
		if err := rows.Scan(
			&t.ID, &t.Slug, &t.Name, &t.Country, &t.Sites,
		); err != nil {
			return nil, fmt.Errorf("scan tenant: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	if err := s.record(
		ctx,
		tx,
		actorExternalID,
		"platform.read",
		map[string]any{
			"entity": "tenants",
			"count":  len(out),
		},
	); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit platform tx: %w", err)
	}
	return out, nil
}

func (s *Store) record(
	ctx context.Context,
	tx pgx.Tx,
	actor, action string,
	detail map[string]any,
) error {
	encoded, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("encode platform audit detail: %w", err)
	}
	_, err = tx.Exec(ctx,
		`insert into platform_audit_log (actor_external_id, action, detail)
		 values ($1, $2, $3)`, actor, action, encoded)
	if err != nil {
		return fmt.Errorf("insert platform audit entry: %w", err)
	}
	return nil
}

// TenantIDs lists every operator, for background jobs to iterate.
//
// Deliberately not audited, unlike ListTenants: this returns identifiers only,
// no operator data, and it runs on a ticker. Auditing it would bury the reads
// that matter — a human looking at another campsite's records — under a stream
// of machine noise.
func (s *Store) TenantIDs(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `select id::text from tenants order by slug`)
	if err != nil {
		return nil, fmt.Errorf("list tenant ids: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan tenant id: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
