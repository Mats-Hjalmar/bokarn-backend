package httpx

import (
	"errors"
	"net/http"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/inventory"
	"github.com/joakimcarlsson/minmux/openapi"
	"github.com/joakimcarlsson/minmux/router"
)

type calendarParams struct {
	From string `query:"from" desc:"Window start, YYYY-MM-DD"`
	To   string `query:"to"   desc:"Window end, exclusive, YYYY-MM-DD"`
}

// BlockRequest takes a unit out of service. Kept separate from a booking: a
// block has no guest, no price and no cancellation policy.
type BlockRequest struct {
	UnitID    string `json:"unit_id"`
	Arrival   string `json:"arrival"   desc:"First night, YYYY-MM-DD"`
	Departure string `json:"departure" desc:"Departure day, exclusive"`
	Reason    string `json:"reason"    desc:"Shown on the tape chart"`
}

type blockParams struct {
	ID string `path:"id" desc:"Allocation UUID"`
}

func (s *Server) registerInventoryAdmin() {
	s.router.Get("/api/v1/admin/categories", s.listCategories,
		openapi.Summary("Categories with their unit counts"),
		openapi.Description(staffNote),
		openapi.Tags("Inventory"),
		openapi.Security(staffScheme),
		openapi.ReturnsBody[[]inventory.Category](http.StatusOK, "Categories"),
		openapi.ReturnsBody[router.ProblemDetails](
			http.StatusUnauthorized, "Missing or invalid session token"),
	)

	s.router.Get("/api/v1/admin/units", s.listUnits,
		openapi.Summary("Units, including retired ones"),
		openapi.Description(staffNote),
		openapi.Tags("Inventory"),
		openapi.Security(staffScheme),
		openapi.ReturnsBody[[]inventory.Unit](http.StatusOK, "Units"),
		openapi.ReturnsBody[router.ProblemDetails](
			http.StatusUnauthorized, "Missing or invalid session token"),
	)

	s.router.Get("/api/v1/admin/calendar", s.calendar,
		openapi.Summary("The tape chart: units and what occupies them"),
		openapi.Description(
			staffNote+" Returns every active unit with the allocations "+
				"overlapping the window, which is the whole read model behind "+
				"the calendar.",
		),
		openapi.Tags("Inventory"),
		openapi.Security(staffScheme),
		openapi.ReturnsBody[[]inventory.CalendarRow](http.StatusOK, "Rows"),
		openapi.ReturnsBody[router.ProblemDetails](
			http.StatusBadRequest, "Bad window"),
	)

	s.router.Post(
		"/api/v1/admin/blocks",
		s.createBlock,
		openapi.Summary("Take a unit out of service"),
		openapi.Description(
			staffNote+" Rejected with 409 when the range collides with an "+
				"existing booking, hold or block — the database enforces that, "+
				"not this handler.",
		),
		openapi.Tags("Inventory"),
		openapi.Security(staffScheme, PermInventory),
		openapi.ReturnsBody[inventory.Allocation](
			http.StatusCreated,
			"Created",
		),
		openapi.ReturnsBody[router.ProblemDetails](
			http.StatusConflict,
			"The unit is already occupied for those dates",
		),
		openapi.ReturnsBody[router.ProblemDetails](
			http.StatusBadRequest, "Bad dates or unknown unit"),
		openapi.ReturnsBody[router.ProblemDetails](
			http.StatusForbidden, "Missing inventory.manage"),
	)

	s.router.Delete("/api/v1/admin/blocks/{id}", s.deleteBlock,
		openapi.Summary("Put a unit back in service"),
		openapi.Description(staffNote+" Blocks only; a booking is cancelled "+
			"through the booking API, which has a policy to apply."),
		openapi.Tags("Inventory"),
		openapi.Security(staffScheme, PermInventory),
		openapi.Returns(http.StatusNoContent, "Removed"),
		openapi.ReturnsBody[router.ProblemDetails](
			http.StatusNotFound, "No such block"),
	)
}

func (s *Server) listCategories(c *router.Context) {
	categories, err := s.inventory.ListCategories(c.Ctx())
	if err != nil {
		logger.ErrorContext(c.Ctx(), "list categories failed", "err", err)
		writeProblem(c, router.InternalServerError("could not list categories"))
		return
	}
	c.JSON(http.StatusOK, categories)
}

func (s *Server) listUnits(c *router.Context) {
	units, err := s.inventory.ListUnits(c.Ctx())
	if err != nil {
		logger.ErrorContext(c.Ctx(), "list units failed", "err", err)
		writeProblem(c, router.InternalServerError("could not list units"))
		return
	}
	c.JSON(http.StatusOK, units)
}

func (s *Server) calendar(c *router.Context, p calendarParams) {
	from, to := p.From, p.To
	if from == "" || to == "" {
		// A missing window is a mistake worth a clear answer rather than a
		// default that quietly returns the wrong month.
		writeProblem(c, router.BadRequest("from and to are required"))
		return
	}
	if _, _, err := parseStay(from, to); err != nil {
		writeProblem(c, router.BadRequest(err.Error()))
		return
	}

	rows, err := s.inventory.Calendar(c.Ctx(), from, to)
	if err != nil {
		logger.ErrorContext(c.Ctx(), "calendar failed", "err", err)
		writeProblem(
			c,
			router.InternalServerError("could not read the calendar"),
		)
		return
	}
	c.JSON(http.StatusOK, rows)
}

func (s *Server) createBlock(c *router.Context) {
	var req BlockRequest
	if err := decodeJSON(c, &req); err != nil {
		writeProblem(c, router.BadRequest(err.Error()))
		return
	}
	if req.UnitID == "" {
		writeProblem(c, router.BadRequest("unit_id is required"))
		return
	}
	if _, _, err := parseStay(req.Arrival, req.Departure); err != nil {
		writeProblem(c, router.BadRequest(err.Error()))
		return
	}

	block, err := s.inventory.CreateBlock(c.Ctx(), inventory.NewBlock{
		UnitID:    req.UnitID,
		Arrival:   req.Arrival,
		Departure: req.Departure,
		Reason:    req.Reason,
	})
	switch {
	case err == nil:
		c.JSON(http.StatusCreated, block)
	case errors.Is(err, inventory.ErrOverlap):
		writeProblem(c, router.Conflict(
			"that unit is already occupied for those dates"))
	case errors.Is(err, inventory.ErrNotFound):
		writeProblem(c, router.BadRequest("unknown unit"))
	default:
		logger.ErrorContext(c.Ctx(), "create block failed", "err", err)
		writeProblem(
			c,
			router.InternalServerError("could not create the block"),
		)
	}
}

func (s *Server) deleteBlock(c *router.Context, p blockParams) {
	err := s.inventory.DeleteBlock(c.Ctx(), p.ID)
	switch {
	case err == nil:
		c.NoContent()
	case errors.Is(err, inventory.ErrNotFound):
		writeProblem(c, router.NotFound("no such block"))
	default:
		logger.ErrorContext(c.Ctx(), "delete block failed", "err", err)
		writeProblem(
			c,
			router.InternalServerError("could not remove the block"),
		)
	}
}
