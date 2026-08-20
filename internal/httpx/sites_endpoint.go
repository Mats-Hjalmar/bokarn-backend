package httpx

import (
	"net/http"
	"time"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/inventory"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/tenant"
	"github.com/joakimcarlsson/minmux/openapi"
	"github.com/joakimcarlsson/minmux/router"
)

// sitesCacheTTL is short on purpose. Cache tags are fixed per route rather than
// per operator, so there is no way to invalidate one campsite's entry without
// clearing every campsite's; a small TTL is the honest alternative.
const sitesCacheTTL = time.Minute

func (s *Server) registerSites() {
	// Public: the operator comes from the hostname, and the response is
	// cached. This route exists partly to prove the cache is keyed by operator
	// — the same path on two hostnames must not share an entry.
	s.router.Get("/api/v1/sites", s.listSites,
		openapi.Summary("Sites of the operator being browsed"),
		openapi.Description(
			"The operator is resolved from the request hostname. Cached "+
				"briefly, keyed by operator.",
		),
		openapi.Tags("Inventory"),
		openapi.NoSecurity(),
		tenant.Cached(sitesCacheTTL, nil),
		openapi.ReturnsBody[[]inventory.Site](http.StatusOK, "Sites"),
		openapi.ReturnsBody[router.ProblemDetails](
			http.StatusBadRequest, "The hostname names no operator",
		),
	)

	s.router.Get("/api/v1/admin/sites", s.listSites,
		openapi.Summary("Sites of the signed-in operator"),
		openapi.Description(staffNote+" The operator comes from the session."),
		openapi.Tags("Inventory"),
		openapi.Security(staffScheme),
		openapi.ReturnsBody[[]inventory.Site](http.StatusOK, "Sites"),
		openapi.ReturnsBody[router.ProblemDetails](
			http.StatusUnauthorized, "Missing or invalid session token",
		),
	)
}

func (s *Server) listSites(c *router.Context) {
	sites, err := s.inventory.ListSites(c.Ctx())
	if err != nil {
		if isNoTenant(err) {
			writeProblem(c, router.BadRequest("no operator selected"))
			return
		}
		logger.ErrorContext(c.Ctx(), "list sites failed", "err", err)
		writeProblem(c, router.InternalServerError("could not list sites"))
		return
	}
	c.JSON(http.StatusOK, sites)
}
