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

// ReservedSlugs are first labels that name a service rather than an operator.
//
// Operators and services share one hostname namespace — a guest reaches
// storsand.bokarn.se and the API answers on api.bokarn.se — which keeps guest
// URLs looking the way they should in production. The cost is this list.
// Without it the dashboard's own calls to api.<domain> resolve to an operator
// named "api", and a staff token is then refused for naming a different
// operator than its session: a failure that is confusing precisely because
// nothing about it looks wrong.
//
// Every hostname in deploy/dev-proxy.caddy belongs here, and an operator slug
// must never collide with one. SlugReserved is exported so operator creation
// can refuse them at the source rather than leaving it to be discovered as a
// routing oddity.
var ReservedSlugs = map[string]struct{}{
	"api":              {},
	"auth":             {},
	"auth-admin":       {},
	"auth-staff":       {},
	"auth-staff-admin": {},
	"dashboard":        {},
	"dev-proxy":        {},
	"grafana":          {},
	"localhost":        {},
	"mail":             {},
	"otlp":             {},
	"web":              {},
	"www":              {},
}

// SlugReserved reports whether a slug names a service and so cannot be an
// operator's.
func SlugReserved(slug string) bool {
	_, reserved := ReservedSlugs[strings.ToLower(slug)]
	return reserved
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
	if slug == "" || SlugReserved(slug) {
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
