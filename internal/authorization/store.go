package authorization

import (
	"context"
	"fmt"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Store reads roles and their permissions within the pinned operator.
type Store struct {
	db *tenant.DB
}

// NewStore constructs a Store over the tenant-pinned handle.
func NewStore(d *tenant.DB) *Store { return &Store{db: d} }

// PermissionsForUser returns every permission key the user holds through their
// roles. A user with no roles holds none, which is a readable account rather
// than a broken one: routes that declare no permission are open to any member
// of the operator, and everything that changes state declares one.
func (s *Store) PermissionsForUser(
	ctx context.Context,
	userID string,
) (map[string]struct{}, error) {
	granted := make(map[string]struct{})

	err := s.db.ReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`select distinct rp.permission_key
			   from user_roles ur
			   join role_permissions rp on rp.role_id = ur.role_id
			  where ur.user_id = $1`, userID)
		if err != nil {
			return fmt.Errorf("query permissions: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				return fmt.Errorf("scan permission: %w", err)
			}
			granted[key] = struct{}{}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return granted, nil
}

// HasAll reports whether the user holds every required permission.
func (s *Store) HasAll(
	ctx context.Context,
	userID string,
	required []string,
) (bool, error) {
	if len(required) == 0 {
		return true, nil
	}
	granted, err := s.PermissionsForUser(ctx, userID)
	if err != nil {
		return false, err
	}
	for _, key := range required {
		if _, ok := granted[key]; !ok {
			return false, nil
		}
	}
	return true, nil
}

// RolesForUser returns the roles held by a user, for the profile endpoint.
func (s *Store) RolesForUser(
	ctx context.Context,
	userID string,
) ([]Role, error) {
	var roles []Role
	err := s.db.ReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`select r.id::text, r.name, r.description
			   from user_roles ur
			   join roles r on r.id = ur.role_id
			  where ur.user_id = $1
			  order by r.name`, userID)
		if err != nil {
			return fmt.Errorf("query roles: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var r Role
			if err := rows.Scan(&r.ID, &r.Name, &r.Description); err != nil {
				return fmt.Errorf("scan role: %w", err)
			}
			roles = append(roles, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return roles, nil
}
