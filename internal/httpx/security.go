package httpx

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/account"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/authentication"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/authorization"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/tenant"
	"github.com/joakimcarlsson/minmux/auth"
	"github.com/joakimcarlsson/minmux/openapi"
)

// The three identity populations. Guests and staff live on separate Ory Kratos
// instances with separate stores, so a guest session can never satisfy a staff
// route no matter how a token is replayed. Platform is bokarn's own operators.
const (
	guestScheme    = "guest"
	staffScheme    = "staff"
	platformScheme = "platform"
)

// Staff permissions. These constants are mirrored by
// dashboard/src/lib/auth/access.ts, which drives the sidebar — keep the two
// lists in step. They are also rows in the permissions table: a key that is not
// seeded there cannot be granted.
//
// A route that declares the staff scheme with no permission is readable by any
// member of the operator. Permissions gate the operations that change
// something, which is what makes an empty role a read-only role.
const (
	PermSettings  = "settings.manage"
	PermInventory = "inventory.manage"
	PermPricing   = "pricing.manage"
	PermBookings  = "bookings.manage"
	PermFrontdesk = "frontdesk.operate"
	PermBilling   = "billing.manage"
	PermLoyalty   = "loyalty.manage"
	PermGuests    = "guests.read_registration"
	PermAudit     = "audit.read"
)

const staffNote = "Staff only: requires a staff session token, and the " +
	"permission this operation declares if it declares one. A missing or " +
	"invalid token is 401; a valid token without the permission is 403."

// staffPrincipal is what a verified staff request carries.
type staffPrincipal struct {
	UserID   string
	TenantID tenant.ID
	Session  authentication.Session
}

// guestPrincipal is what a verified guest request carries. It has no operator:
// a guest may hold bookings at several campsites under one login, so the
// operator comes from the host being browsed.
type guestPrincipal struct {
	Session authentication.Session
}

// platformPrincipal is one of bokarn's own operators.
type platformPrincipal struct {
	Session authentication.Session
}

func registerSecuritySchemes(gen *openapi.Generator) {
	gen.SecuritySchemes = map[string]*openapi.SecurityScheme{
		guestScheme: openapi.BearerAuth(
			"Ory",
			"Guest Ory Kratos session token, sent as a Bearer credential.",
		),
		staffScheme: openapi.BearerAuth(
			"Ory",
			"Staff (dashboard) Ory Kratos session token, sent as a Bearer credential.",
		),
		platformScheme: openapi.BearerAuth(
			"Ory",
			"bokarn platform-operator session token. Grants audited cross-tenant reads.",
		),
	}
}

// staffVerifier resolves a staff session, pins the operator it names, ensures
// the local user row exists, and checks the route's declared permissions.
//
// The order matters and cannot be rearranged: the operator has to be pinned
// before any query runs, and the session is the only thing that names it.
func staffVerifier(
	resolver *authentication.Resolver,
	accounts *account.Store,
	authz *authorization.Store,
) auth.Verifier {
	return func(r *http.Request, scopes []string) (any, error) {
		token, ok := bearerToken(r)
		if !ok {
			return nil, auth.ErrNoCredential
		}

		session, err := resolver.Resolve(r.Context(), token)
		if err != nil {
			return nil, err
		}

		// An identity with no operator is refused rather than defaulted. A
		// staff account that is not attached to a campsite has no business
		// reading one.
		if session.TenantID == "" {
			return nil, auth.ErrForbidden
		}

		id := tenant.ID(session.TenantID)
		ctx := tenant.With(r.Context(), id)

		user, err := accounts.Provision(
			ctx, session.ExternalUserID, session.Email, session.Name,
		)
		if err != nil {
			if errors.Is(err, account.ErrForeignIdentity) {
				return nil, auth.ErrForbidden
			}
			return nil, err
		}

		if len(scopes) > 0 {
			allowed, err := authz.HasAll(ctx, user.ID, scopes)
			if err != nil {
				return nil, err
			}
			if !allowed {
				return nil, auth.ErrForbidden
			}
		}

		return staffPrincipal{
			UserID: user.ID, TenantID: id, Session: session,
		}, nil
	}
}

// guestVerifier resolves a guest session. It performs no tenant work at all:
// the operator comes from the host, and the guest's per-operator record is
// created when they first book there.
func guestVerifier(resolver *authentication.Resolver) auth.Verifier {
	return func(r *http.Request, _ []string) (any, error) {
		token, ok := bearerToken(r)
		if !ok {
			return nil, auth.ErrNoCredential
		}
		session, err := resolver.Resolve(r.Context(), token)
		if err != nil {
			return nil, err
		}
		return guestPrincipal{Session: session}, nil
	}
}

// platformVerifier admits only identities explicitly marked as bokarn's own.
// The flag lives in metadata_public, which only the Kratos admin API can write.
func platformVerifier(resolver *authentication.Resolver) auth.Verifier {
	return func(r *http.Request, _ []string) (any, error) {
		token, ok := bearerToken(r)
		if !ok {
			return nil, auth.ErrNoCredential
		}
		session, err := resolver.Resolve(r.Context(), token)
		if err != nil {
			return nil, err
		}
		if !session.Platform {
			return nil, auth.ErrForbidden
		}
		return platformPrincipal{Session: session}, nil
	}
}

// tenantFromIdentity is the authoritative operator for an authenticated staff
// request. Guests and platform operators deliberately yield nothing.
func tenantFromIdentity(r *http.Request) (tenant.ID, bool, error) {
	p, ok := auth.PrincipalFor[staffPrincipal](r.Context(), staffScheme)
	if !ok {
		return "", false, nil
	}
	return p.TenantID, true, nil
}

func bearerToken(r *http.Request) (string, bool) {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if len(header) <= len(prefix) ||
		!strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	if token == "" {
		return "", false
	}
	return token, true
}
