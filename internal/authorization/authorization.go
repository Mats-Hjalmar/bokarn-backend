// Package authorization evaluates what a staff member may do inside their
// operator. Permissions are a fixed global vocabulary the handlers declare;
// roles are per-operator bundles of them. Identity comes from authentication,
// access rights live here.
package authorization

import "errors"

// ErrRoleNotFound is returned when no role matches the lookup.
var ErrRoleNotFound = errors.New("authorization: role not found")

// Role is one operator's named bundle of permissions.
type Role struct {
	ID          string
	Name        string
	Description string
	Permissions []string
}
