// Package account owns the staff user record inside one operator. A user is
// provisioned automatically on first sight of a valid session; membership of an
// operator never is — that is decided when the identity is created, and this
// package only reflects it.
package account

import "errors"

// ErrUserNotFound is returned when no user matches the lookup.
var ErrUserNotFound = errors.New("account: user not found")

// ErrForeignIdentity is returned when the identity already belongs to a
// different operator. External ids are globally unique so that one person maps
// to exactly one operator; a second claim is a configuration error, never a
// reason to move them.
var ErrForeignIdentity = errors.New(
	"account: identity belongs to another operator",
)

// User is a staff member as this operator sees them.
type User struct {
	ID             string
	ExternalUserID string
	Email          string
	Name           string
}
