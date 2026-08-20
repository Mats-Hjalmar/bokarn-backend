// Package authentication verifies Ory Kratos session tokens. It answers who the
// caller is and, for staff, which operator they belong to; what they may do is
// decided by the authorization package.
//
// It deliberately touches no database. Resolving the session is what yields the
// operator, and the operator has to be pinned before any query can run — so
// provisioning the local user is the caller's job, in that order.
package authentication

import (
	"context"
	"errors"
	"sync"
	"time"

	ory "github.com/ory/client-go"
)

// ErrNoSession is returned when the token is absent, inactive, or unknown to
// this Kratos instance.
var ErrNoSession = errors.New("authentication: no active session")

// Audiences namespace the two identity populations. They live on separate
// Kratos instances with separate stores, and the prefix keeps their local user
// rows from ever colliding.
const (
	AudienceStaff = "staff"
	AudienceGuest = "guest"
)

// Session is a verified caller.
type Session struct {
	// ExternalUserID is the audience-qualified identity id, as stored in
	// users.external_user_id.
	ExternalUserID string
	// TenantID is the operator a staff session belongs to. Empty for guests:
	// a guest may book at several operators with one login, so their operator
	// comes from the host being browsed, not from who they are. Whether an
	// empty value is acceptable is the verifier's call, not this package's.
	TenantID string
	// Platform marks one of bokarn's own operators, who may read across
	// tenants through the platform routes.
	Platform bool
	Email    string
	Name     string
}

// ExternalID composes the stored identifier for an identity in an audience.
func ExternalID(audience, identityID string) string {
	return audience + ":" + identityID
}

type clock func() time.Time

// Resolver verifies session tokens against one Kratos instance and caches the
// result for its configured TTL.
type Resolver struct {
	frontend ory.FrontendAPI
	audience string
	ttl      time.Duration
	now      clock

	mu    sync.Mutex
	cache map[string]entry
}

type entry struct {
	session Session
	expires time.Time
}

// New constructs a Resolver for the Kratos instance at publicURL.
func New(publicURL, audience string, ttl time.Duration) *Resolver {
	cfg := ory.NewConfiguration()
	cfg.Servers = ory.ServerConfigurations{{URL: publicURL}}
	return &Resolver{
		frontend: ory.NewAPIClient(cfg).FrontendAPI,
		audience: audience,
		ttl:      ttl,
		now:      time.Now,
		cache:    make(map[string]entry),
	}
}

// Resolve validates the token and returns the caller.
func (r *Resolver) Resolve(
	ctx context.Context,
	sessionToken string,
) (Session, error) {
	if s, ok := r.cached(sessionToken); ok {
		return s, nil
	}

	session, _, err := r.frontend.ToSession(ctx).
		XSessionToken(sessionToken).
		Execute()
	if err != nil {
		return Session{}, ErrNoSession
	}
	if session.Active == nil || !*session.Active || session.Identity == nil {
		return Session{}, ErrNoSession
	}

	id := session.Identity
	out := Session{
		ExternalUserID: ExternalID(r.audience, id.Id),
	}
	out.Email, out.Name = traits(id.Traits)

	// metadata_public rather than metadata_admin: only the former is returned
	// by the public session endpoint. Both are writable exclusively through the
	// admin API, so it is equally trustworthy — it is merely visible to its
	// owner, and which campsite someone works at is not a secret from them.
	out.TenantID, _ = metadataString(id.MetadataPublic, "tenant_id")
	out.Platform, _ = id.MetadataPublic["platform"].(bool)

	r.remember(sessionToken, out)
	return out, nil
}

func traits(raw any) (email, name string) {
	m, ok := raw.(map[string]any)
	if !ok {
		return "", ""
	}
	email, _ = m["email"].(string)
	name, _ = m["name"].(string)
	return email, name
}

func metadataString(raw map[string]any, key string) (string, bool) {
	if raw == nil {
		return "", false
	}
	v, ok := raw[key].(string)
	if !ok || v == "" {
		return "", false
	}
	return v, true
}

func (r *Resolver) cached(token string) (Session, bool) {
	if r.ttl <= 0 {
		return Session{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.cache[token]
	if !ok {
		return Session{}, false
	}
	if r.now().After(e.expires) {
		delete(r.cache, token)
		return Session{}, false
	}
	return e.session, true
}

func (r *Resolver) remember(token string, s Session) {
	if r.ttl <= 0 {
		return
	}
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, e := range r.cache {
		if now.After(e.expires) {
			delete(r.cache, k)
		}
	}
	r.cache[token] = entry{session: s, expires: now.Add(r.ttl)}
}
