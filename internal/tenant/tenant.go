// Package tenant carries the operator identity of a request and is the only
// route from a domain package to Postgres. It owns the context key, the
// transaction helpers that pin the tenant for the database to enforce, and the
// output-cache wrapper that keeps one operator's response out of another's
// cache entry. It deliberately knows nothing about HTTP authentication: how a
// request's tenant is discovered is supplied to Middleware as a function.
package tenant

import (
	"context"
	"errors"
)

// ErrNoTenant is returned when a code path that needs an operator runs without
// one pinned. It is never recovered from by choosing a default: an unpinned
// request must fail rather than read someone's data.
var ErrNoTenant = errors.New("tenant: no tenant in context")

// ID is an operator's identifier, as stored in tenants.id.
type ID string

func (i ID) String() string { return string(i) }

type contextKey struct{}

// With returns a context carrying the operator id.
func With(ctx context.Context, id ID) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

// FromContext returns the operator pinned on ctx, or ErrNoTenant.
func FromContext(ctx context.Context) (ID, error) {
	id, ok := ctx.Value(contextKey{}).(ID)
	if !ok || id == "" {
		return "", ErrNoTenant
	}
	return id, nil
}

// MaybeFromContext returns the operator pinned on ctx, if any. It exists for
// the cache key and for logging, where the absence of a tenant is a normal
// state rather than an error.
func MaybeFromContext(ctx context.Context) (ID, bool) {
	id, ok := ctx.Value(contextKey{}).(ID)
	return id, ok && id != ""
}
