package httpx

import (
	"net/http"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/platform"
	"github.com/joakimcarlsson/minmux/auth"
	"github.com/joakimcarlsson/minmux/openapi"
	"github.com/joakimcarlsson/minmux/router"
)

func (s *Server) registerPlatform() {
	s.router.Get("/api/v1/platform/tenants", s.listTenants,
		openapi.Summary("All operators"),
		openapi.Description(
			"bokarn platform operators only. This is the one path that reads "+
				"across operators, and every call is recorded in "+
				"platform_audit_log before it returns.",
		),
		openapi.Tags("Platform"),
		openapi.Security(platformScheme),
		openapi.ReturnsBody[[]platform.Tenant](http.StatusOK, "Operators"),
		openapi.ReturnsBody[router.ProblemDetails](
			http.StatusUnauthorized, "Missing or invalid session token",
		),
		openapi.ReturnsBody[router.ProblemDetails](
			http.StatusForbidden, "Not a platform operator",
		),
	)
}

func (s *Server) listTenants(c *router.Context) {
	ctx := c.Ctx()

	p, ok := auth.PrincipalFor[platformPrincipal](ctx, platformScheme)
	if !ok {
		writeProblem(c, router.Unauthorized("no platform session"))
		return
	}

	tenants, err := s.platform.ListTenants(ctx, p.Session.ExternalUserID)
	if err != nil {
		logger.ErrorContext(ctx, "list tenants failed", "err", err)
		writeProblem(c, router.InternalServerError("could not list operators"))
		return
	}
	c.JSON(http.StatusOK, tenants)
}
