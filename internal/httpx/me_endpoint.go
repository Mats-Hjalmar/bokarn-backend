package httpx

import (
	"net/http"
	"sort"

	"github.com/joakimcarlsson/minmux/auth"
	"github.com/joakimcarlsson/minmux/openapi"
	"github.com/joakimcarlsson/minmux/router"
)

// MeResponse is the signed-in staff member, the operator they act for, and
// what they are allowed to do. The dashboard drives its navigation from
// permissions, so this is the one call it makes before rendering anything.
type MeResponse struct {
	User        MeUser   `json:"user"`
	Tenant      MeTenant `json:"tenant"`
	Roles       []string `json:"roles"       desc:"Role names in this operator"`
	Permissions []string `json:"permissions" desc:"Permission keys, sorted"`
}

// MeUser is the caller.
type MeUser struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

// MeTenant is the operator the caller acts for. It comes from the session, not
// from the request, so it cannot be switched by changing a hostname.
type MeTenant struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

func (s *Server) registerMe() {
	s.router.Get("/api/v1/me", s.me,
		openapi.Summary("The signed-in staff member"),
		openapi.Description(
			staffNote+" Returns the caller, the operator their session names, "+
				"and every permission they hold there.",
		),
		openapi.Tags("Identity"),
		openapi.Security(staffScheme),
		openapi.ReturnsBody[MeResponse](http.StatusOK, "The caller"),
		openapi.ReturnsBody[router.ProblemDetails](
			http.StatusUnauthorized, "Missing or invalid session token",
		),
	)
}

func (s *Server) me(c *router.Context) {
	ctx := c.Ctx()

	p, ok := auth.PrincipalFor[staffPrincipal](ctx, staffScheme)
	if !ok {
		writeProblem(c, router.Unauthorized("no staff session"))
		return
	}

	granted, err := s.authz.PermissionsForUser(ctx, p.UserID)
	if err != nil {
		logger.ErrorContext(ctx, "read permissions failed", "err", err)
		writeProblem(
			c,
			router.InternalServerError("could not read permissions"),
		)
		return
	}
	permissions := make([]string, 0, len(granted))
	for key := range granted {
		permissions = append(permissions, key)
	}
	sort.Strings(permissions)

	roles, err := s.authz.RolesForUser(ctx, p.UserID)
	if err != nil {
		logger.ErrorContext(ctx, "read roles failed", "err", err)
		writeProblem(c, router.InternalServerError("could not read roles"))
		return
	}
	names := make([]string, 0, len(roles))
	for _, r := range roles {
		names = append(names, r.Name)
	}

	operator, err := s.tenants.Current(ctx)
	if err != nil {
		logger.ErrorContext(ctx, "read operator failed", "err", err)
		writeProblem(c, router.InternalServerError("could not read operator"))
		return
	}

	c.JSON(http.StatusOK, MeResponse{
		User: MeUser{
			ID:    p.UserID,
			Email: p.Session.Email,
			Name:  p.Session.Name,
		},
		Tenant: MeTenant{
			ID: operator.ID, Slug: operator.Slug, Name: operator.Name,
		},
		Roles:       names,
		Permissions: permissions,
	})
}
