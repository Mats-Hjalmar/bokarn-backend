package account

import (
	"context"
	"errors"
	"fmt"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/db"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Store reads and provisions staff users.
type Store struct {
	db *tenant.DB
}

// NewStore constructs a Store over the tenant-pinned handle.
func NewStore(d *tenant.DB) *Store { return &Store{db: d} }

// ByExternalID returns the user for a Kratos identity within the pinned
// operator.
func (s *Store) ByExternalID(ctx context.Context, ext string) (User, error) {
	var u User
	err := s.db.ReadTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`select id::text, external_user_id,
			        coalesce(email, ''), coalesce(name, '')
			   from users where external_user_id = $1`, ext,
		).Scan(&u.ID, &u.ExternalUserID, &u.Email, &u.Name)
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, ErrUserNotFound
		}
		return User{}, fmt.Errorf("query user: %w", err)
	}
	return u, nil
}

// refresh copies the identity's current name and address onto an existing row.
//
// nullif and coalesce together mean an identity that supplies no name cannot
// blank one that is already there: absent is not the same as cleared, and only
// the first is what a missing trait tells us.
func (s *Store) refresh(
	ctx context.Context,
	u User,
	email, name string,
) (User, error) {
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			update users
			   set email = coalesce(nullif($2, ''), email),
			       name  = coalesce(nullif($3, ''), name)
			 where id = $1
			returning id::text, external_user_id,
			          coalesce(email, ''), coalesce(name, '')`,
			u.ID, email, name,
		).Scan(&u.ID, &u.ExternalUserID, &u.Email, &u.Name)
	})
	if err != nil {
		return User{}, fmt.Errorf("refresh user: %w", err)
	}
	return u, nil
}

// Provision returns the user for a Kratos identity, inserting the row on first
// sight and keeping its name and address in step after that. It is safe to call
// concurrently: a lost insert race re-reads.
//
// The refresh is not decoration. A user row can exist before its owner has ever
// signed in — the dev seed creates one, and an invite flow would too — and the
// first version of this only wrote email and name on insert. The result was a
// whole operator's staff with no names, which shows up as an audit trail
// recording "staff" for every action instead of who took it. The identity
// provider is the source of truth for a person's name; this keeps a copy, so it
// has to keep it current.
func (s *Store) Provision(
	ctx context.Context,
	ext, email, name string,
) (User, error) {
	u, err := s.ByExternalID(ctx, ext)
	if err == nil {
		if u.Email == email && u.Name == name {
			return u, nil
		}
		return s.refresh(ctx, u, email, name)
	}
	if !errors.Is(err, ErrUserNotFound) {
		return User{}, err
	}

	err = s.db.Tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`insert into users (external_user_id, email, name)
			 values ($1, $2, $3)
			 returning id::text, external_user_id,
			           coalesce(email, ''), coalesce(name, '')`,
			ext, email, name,
		).Scan(&u.ID, &u.ExternalUserID, &u.Email, &u.Name)
	})
	if err == nil {
		return u, nil
	}

	// A unique violation here means the external id is already taken. Either we
	// lost a race inside this operator, or the identity belongs to another one
	// — and those must not be conflated, because the second is a
	// misconfiguration that would otherwise look like a successful login.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == db.UniqueViolation {
		u, readErr := s.ByExternalID(ctx, ext)
		if readErr == nil {
			return u, nil
		}
		if errors.Is(readErr, ErrUserNotFound) {
			return User{}, ErrForeignIdentity
		}
		return User{}, readErr
	}
	return User{}, fmt.Errorf("provision user: %w", err)
}

// TouchLastSeen records activity. A failure here is reported to the caller
// rather than swallowed: it means the write path is broken, not that the user
// is idle.
func (s *Store) TouchLastSeen(ctx context.Context, userID string) error {
	return s.db.Tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`update users set last_seen_at = now() where id = $1`, userID)
		if err != nil {
			return fmt.Errorf("touch last seen: %w", err)
		}
		return nil
	})
}
