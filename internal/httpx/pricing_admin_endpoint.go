package httpx

import (
	"errors"
	"net/http"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/pricing"
	"github.com/joakimcarlsson/minmux/openapi"
	"github.com/joakimcarlsson/minmux/router"
)

type rateCalendarParams struct {
	Plan string `query:"plan" desc:"Rate plan UUID"`
	From string `query:"from" desc:"Window start, YYYY-MM-DD"`
	To   string `query:"to"   desc:"Window end, exclusive"`
}

type seasonParams struct {
	ID string `path:"id" desc:"Season UUID"`
}

// CompileRequest recompiles the rate calendar over a window.
type CompileRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// CompileResponse reports how many nights were written.
type CompileResponse struct {
	Days int `json:"days"`
}

func (s *Server) registerPricingAdmin() {
	s.router.Get("/api/v1/admin/rates/plans", s.listRatePlans,
		openapi.Summary("Rate plans with their seasons"),
		openapi.Description(staffNote+" The authoring surface: broad seasons "+
			"rather than the compiled per-night calendar."),
		openapi.Tags("Pricing"),
		openapi.Security(staffScheme),
		openapi.ReturnsBody[[]pricing.Plan](http.StatusOK, "Plans"),
	)

	s.router.Get("/api/v1/admin/rates/calendar", s.rateCalendar,
		openapi.Summary("The compiled rate calendar"),
		openapi.Description(staffNote+" One row per night, each naming the "+
			"season that produced it — which is the answer to why a date "+
			"costs what it costs."),
		openapi.Tags("Pricing"),
		openapi.Security(staffScheme),
		openapi.ReturnsBody[[]pricing.CalendarDay](http.StatusOK, "Nights"),
		openapi.ReturnsBody[router.ProblemDetails](
			http.StatusBadRequest, "Bad window or missing plan"),
	)

	s.router.Patch(
		"/api/v1/admin/rates/seasons/{id}",
		s.updateSeason,
		openapi.Summary("Reprice a season"),
		openapi.Description(staffNote+" Recompiles the affected plan, so the "+
			"change reaches the rate calendar in the same request."),
		openapi.Tags("Pricing"),
		openapi.Security(staffScheme, PermPricing),
		openapi.ReturnsBody[CompileResponse](
			http.StatusOK,
			"Nights recompiled",
		),
		openapi.ReturnsBody[router.ProblemDetails](
			http.StatusNotFound, "No such season"),
		openapi.ReturnsBody[router.ProblemDetails](
			http.StatusForbidden, "Missing pricing.manage"),
	)

	s.router.Post("/api/v1/admin/rates/compile", s.compileRates,
		openapi.Summary("Recompile every plan over a window"),
		openapi.Description(staffNote+" Seasons are the authoring surface and "+
			"the rate calendar is the evaluation surface; this turns one into "+
			"the other. Idempotent."),
		openapi.Tags("Pricing"),
		openapi.Security(staffScheme, PermPricing),
		openapi.ReturnsBody[CompileResponse](http.StatusOK, "Nights compiled"),
	)
}

func (s *Server) listRatePlans(c *router.Context) {
	plans, err := s.pricing.ListPlans(c.Ctx())
	if err != nil {
		logger.ErrorContext(c.Ctx(), "list rate plans failed", "err", err)
		writeProblem(c, router.InternalServerError("could not list rate plans"))
		return
	}
	c.JSON(http.StatusOK, plans)
}

func (s *Server) rateCalendar(c *router.Context, p rateCalendarParams) {
	if p.Plan == "" || p.From == "" || p.To == "" {
		writeProblem(c, router.BadRequest("plan, from and to are required"))
		return
	}
	if _, _, err := parseStay(p.From, p.To); err != nil {
		writeProblem(c, router.BadRequest(err.Error()))
		return
	}

	days, err := s.pricing.Calendar(c.Ctx(), p.Plan, p.From, p.To)
	if err != nil {
		logger.ErrorContext(c.Ctx(), "rate calendar failed", "err", err)
		writeProblem(
			c,
			router.InternalServerError("could not read the calendar"),
		)
		return
	}
	c.JSON(http.StatusOK, days)
}

func (s *Server) updateSeason(c *router.Context, p seasonParams) {
	var req pricing.SeasonUpdate
	if err := decodeJSON(c, &req); err != nil {
		writeProblem(c, router.BadRequest(err.Error()))
		return
	}
	switch {
	case req.BaseMinor <= 0:
		writeProblem(c, router.BadRequest("base_minor must be above zero"))
		return
	case req.MinStay < 1 || req.MinStay > 365:
		writeProblem(c, router.BadRequest("min_stay must be between 1 and 365"))
		return
	case req.IncludedAdults < 0 || req.IncludedAdults > 50:
		writeProblem(c, router.BadRequest("included_adults is out of range"))
		return
	case req.AdultExtraMinor < 0 || req.PetMinor < 0:
		writeProblem(c, router.BadRequest("extras cannot be negative"))
		return
	}

	ctx := c.Ctx()
	planID, err := s.pricing.PlanIDForSeason(ctx, p.ID)
	if err != nil {
		writeProblem(c, router.NotFound("no such season"))
		return
	}

	if err := s.pricing.UpdateSeason(ctx, p.ID, req); err != nil {
		if errors.Is(err, pricing.ErrSeasonNotFound) {
			writeProblem(c, router.NotFound("no such season"))
			return
		}
		if errors.Is(err, pricing.ErrInvalidSeason) {
			writeProblem(c, router.BadRequest(
				"one of those values is out of range"))
			return
		}
		logger.ErrorContext(ctx, "update season failed", "err", err)
		writeProblem(
			c,
			router.InternalServerError("could not update the season"),
		)
		return
	}

	// Recompiled immediately: a season edit that did not reach the rate
	// calendar would look applied and sell at the old price.
	days, err := s.pricing.Compile(ctx, planID, compileFrom, compileTo)
	if err != nil {
		logger.ErrorContext(
			ctx,
			"recompile after season edit failed",
			"err",
			err,
		)
		writeProblem(c, router.InternalServerError(
			"the season was saved but the rate calendar could not be rebuilt"))
		return
	}
	c.JSON(http.StatusOK, CompileResponse{Days: days})
}

func (s *Server) compileRates(c *router.Context) {
	var req CompileRequest
	if err := decodeJSON(c, &req); err != nil {
		writeProblem(c, router.BadRequest(err.Error()))
		return
	}
	from, to := req.From, req.To
	if from == "" || to == "" {
		from, to = compileFrom, compileTo
	}
	if _, _, err := parseStay(from, to); err != nil {
		writeProblem(c, router.BadRequest(err.Error()))
		return
	}

	days, err := s.pricing.CompileAll(c.Ctx(), from, to)
	if err != nil {
		logger.ErrorContext(c.Ctx(), "compile rates failed", "err", err)
		writeProblem(c, router.InternalServerError("could not compile rates"))
		return
	}
	c.JSON(http.StatusOK, CompileResponse{Days: days})
}

// The window a season edit recompiles. Wide enough to cover every season a
// campsite has published, narrow enough that a recompile stays a fast
// interactive operation.
const (
	compileFrom = "2026-01-01"
	compileTo   = "2029-12-31"
)
