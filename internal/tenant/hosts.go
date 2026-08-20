package tenant

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// Hosts maps a request hostname to the operator being browsed. This is the
// only tenant source for guests and for unauthenticated routes: a header or
// body value would be attacker-controlled.
type Hosts struct {
	db  *DB
	ttl time.Duration

	mu    sync.RWMutex
	cache map[string]cached
}

type cached struct {
	id      ID
	expires time.Time
}

// NewHosts constructs a resolver caching positive lookups for ttl. Misses are
// deliberately not cached: a hostname that was unknown a second before an
// operator is created must start working immediately, and the failure path is
// already cheap.
func NewHosts(d *DB, ttl time.Duration) *Hosts {
	return &Hosts{db: d, ttl: ttl, cache: make(map[string]cached)}
}

// Source returns the request source for use with Middleware.
func (h *Hosts) Source() Source {
	return func(r *http.Request) (ID, bool, error) {
		slug, ok := SlugFromHost(r.Host)
		if !ok {
			return "", false, nil
		}

		id, err := h.Lookup(r.Context(), slug)
		if err != nil {
			return "", false, err
		}
		return id, true, nil
	}
}

// ErrUnknownOperator is returned when a hostname names no operator. It is an
// error rather than a silent miss so the caller gets a clear 400 instead of a
// request that proceeds unpinned and fails later as a database error.
type ErrUnknownOperator struct{ Slug string }

func (e ErrUnknownOperator) Error() string {
	return fmt.Sprintf("unknown operator %q", e.Slug)
}

// Lookup resolves a slug to an operator id.
//
// It calls tenant_id_for_slug rather than selecting from tenants, because at
// this point no operator is pinned and the policy on that table would return
// nothing. The function is SECURITY DEFINER and exposes only this one mapping.
func (h *Hosts) Lookup(ctx context.Context, slug string) (ID, error) {
	if id, ok := h.get(slug); ok {
		return id, nil
	}

	var id *string
	err := h.db.Pool().QueryRow(ctx,
		`select tenant_id_for_slug($1)::text`, slug,
	).Scan(&id)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("resolve operator slug: %w", err)
	}
	if id == nil {
		return "", ErrUnknownOperator{Slug: slug}
	}

	h.put(slug, ID(*id))
	return ID(*id), nil
}

// SlugFromHost extracts the operator slug from a hostname. A host with no
// operator label yields false, which the caller must treat as "no operator
// selected" rather than defaulting to one.
func SlugFromHost(host string) (string, bool) {
	name, _, found := strings.Cut(host, ":")
	if !found {
		name = host
	}
	labels := strings.Split(name, ".")
	if len(labels) < 2 {
		return "", false
	}
	// DNS is case-insensitive and proxies normalise inconsistently, so the slug
	// is folded before lookup — otherwise STORSAND.bokarn.se is a different
	// operator from storsand.bokarn.se, and each gets its own cache entry.
	slug := strings.ToLower(labels[0])
	if slug == "" || slug == "www" || slug == "localhost" {
		return "", false
	}
	return slug, true
}

func (h *Hosts) get(slug string) (ID, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	c, ok := h.cache[slug]
	if !ok || time.Now().After(c.expires) {
		return "", false
	}
	return c.id, true
}

func (h *Hosts) put(slug string, id ID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cache[slug] = cached{id: id, expires: time.Now().Add(h.ttl)}
}
