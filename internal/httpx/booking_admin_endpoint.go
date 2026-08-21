package httpx

import (
	"errors"
	"net/http"
	"time"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/booking"
	"github.com/joakimcarlsson/minmux/auth"
	"github.com/joakimcarlsson/minmux/openapi"
	"github.com/joakimcarlsson/minmux/router"
)

type bookingListParams struct {
	State  string `query:"state"  desc:"confirmed, checked_in, checked_out…"`
	Search string `query:"search" desc:"Reference, surname or email"`
	Limit  int    `query:"limit"  desc:"Default 100, maximum 200"`
}

type bookingIDParams struct {
	ID string `path:"id" desc:"Booking UUID"`
}

type frontdeskParams struct {
	Date string `query:"date" desc:"Day to work, YYYY-MM-DD. Today by default"`
}

// CancelRequest records why a booking was cancelled. The reason is required:
// "the guest changed their mind" and "we double-booked them" lead to different
// conversations, and a blank reason answers neither.
type CancelRequest struct {
	Reason string `json:"reason"`
}

// ReassignRequest moves a booking to another pitch in the same category.
type ReassignRequest struct {
	UnitID string `json:"unit_id"`
}

func (s *Server) registerBookingAdmin() {
	s.router.Get("/api/v1/admin/bookings", s.listBookings,
		openapi.Summary("Bookings, newest first"),
		openapi.Description(staffNote+" Cancelled bookings are included: the "+
			"list is a record rather than a work queue, and a cancelled "+
			"booking is often exactly what somebody searching a reference "+
			"wants."),
		openapi.Tags("Booking"),
		openapi.Security(staffScheme),
		openapi.ReturnsBody[[]booking.Summary](http.StatusOK, "Bookings"),
		openapi.ReturnsBody[router.ProblemDetails](
			http.StatusUnauthorized, "Missing or invalid session token"),
	)

	s.router.Get(
		"/api/v1/admin/bookings/{id}",
		s.readBookingAdmin,
		openapi.Summary("One booking, with its frozen price and history"),
		openapi.Description(
			staffNote+" The price lines are the ones copied at "+
				"confirmation and can never have been edited; the event log says "+
				"who did what.",
		),
		openapi.Tags("Booking"),
		openapi.Security(staffScheme),
		openapi.ReturnsBody[booking.Detail](http.StatusOK, "The booking"),
		openapi.ReturnsBody[router.ProblemDetails](
			http.StatusNotFound, "No such booking"),
	)

	s.router.Get("/api/v1/admin/frontdesk", s.frontdesk,
		openapi.Summary("Today's arrivals, departures and in-house guests"),
		openapi.Description(staffNote+" Each list is a work queue defined by "+
			"state, so a guest leaves the arrivals list by being checked in "+
			"rather than by anyone ticking them off."),
		openapi.Tags("Front desk"),
		openapi.Security(staffScheme),
		openapi.ReturnsBody[booking.Frontdesk](http.StatusOK, "The day"),
		openapi.ReturnsBody[router.ProblemDetails](
			http.StatusBadRequest, "Bad date"),
	)

	s.router.Post("/api/v1/admin/bookings/{id}/check-in", s.checkIn,
		openapi.Summary("Check a guest in"),
		openapi.Description(staffNote+" Moves the stay to checked_in and pins "+
			"the pitch, which is the point a provisional assignment becomes a "+
			"commitment. A booking that is not confirmed is 409, not an "+
			"error to paper over."),
		openapi.Tags("Front desk"),
		openapi.Security(staffScheme, PermFrontdesk),
		openapi.ReturnsBody[booking.Summary](http.StatusOK, "Checked in"),
		openapi.ReturnsBody[router.ProblemDetails](
			http.StatusConflict, "Not in a state that allows check-in"),
		openapi.ReturnsBody[router.ProblemDetails](
			http.StatusNotFound, "No such booking"),
		openapi.ReturnsBody[router.ProblemDetails](
			http.StatusForbidden, "Missing frontdesk.operate"),
	)

	s.router.Post("/api/v1/admin/bookings/{id}/check-out", s.checkOut,
		openapi.Summary("Check a guest out"),
		openapi.Description(staffNote+" The pitch stays pinned: which pitch "+
			"somebody stayed on is a fact about the past."),
		openapi.Tags("Front desk"),
		openapi.Security(staffScheme, PermFrontdesk),
		openapi.ReturnsBody[booking.Summary](http.StatusOK, "Checked out"),
		openapi.ReturnsBody[router.ProblemDetails](
			http.StatusConflict, "Not checked in"),
		openapi.ReturnsBody[router.ProblemDetails](
			http.StatusNotFound, "No such booking"),
	)

	s.router.Post("/api/v1/admin/bookings/{id}/cancel", s.cancelBooking,
		openapi.Summary("Cancel a booking and release its pitch"),
		openapi.Description(staffNote+" No fee is computed and nothing is "+
			"charged. The cancellation terms were frozen onto the booking at "+
			"confirmation and are applied when there is something able to "+
			"collect them."),
		openapi.Tags("Booking"),
		openapi.Security(staffScheme, PermBookings),
		openapi.ReturnsBody[booking.Summary](http.StatusOK, "Cancelled"),
		openapi.ReturnsBody[router.ProblemDetails](
			http.StatusConflict, "Not in a state that allows cancellation"),
		openapi.ReturnsBody[router.ProblemDetails](
			http.StatusBadRequest, "No reason given"),
	)

	s.router.Post("/api/v1/admin/bookings/{id}/reassign", s.reassignBooking,
		openapi.Summary("Move a booking to another pitch"),
		openapi.Description(staffNote+" The target must be an active unit in "+
			"the same category. A collision is refused by the database, so a "+
			"move onto an occupied pitch is 409 rather than a double booking."),
		openapi.Tags("Booking"),
		openapi.Security(staffScheme, PermBookings),
		openapi.ReturnsBody[booking.Summary](http.StatusOK, "Moved"),
		openapi.ReturnsBody[router.ProblemDetails](
			http.StatusConflict, "That pitch is occupied for those dates"),
		openapi.ReturnsBody[router.ProblemDetails](
			http.StatusBadRequest, "Unknown unit, or wrong category"),
	)
}

func (s *Server) listBookings(c *router.Context, p bookingListParams) {
	bookings, err := s.bookings.List(c.Ctx(), booking.ListFilter{
		State:  p.State,
		Search: p.Search,
		Limit:  p.Limit,
	})
	if err != nil {
		logger.ErrorContext(c.Ctx(), "list bookings failed", "err", err)
		writeProblem(c, router.InternalServerError("could not list bookings"))
		return
	}
	c.JSON(http.StatusOK, bookings)
}

func (s *Server) readBookingAdmin(c *router.Context, p bookingIDParams) {
	detail, err := s.bookings.Detail(c.Ctx(), p.ID)
	switch {
	case err == nil:
		c.JSON(http.StatusOK, detail)
	case errors.Is(err, booking.ErrNotFound):
		writeProblem(c, router.NotFound("no such booking"))
	default:
		logger.ErrorContext(c.Ctx(), "read booking failed", "err", err)
		writeProblem(
			c,
			router.InternalServerError("could not read the booking"),
		)
	}
}

func (s *Server) frontdesk(c *router.Context, p frontdeskParams) {
	date := p.Date
	if date == "" {
		date = time.Now().Format(time.DateOnly)
	}
	if _, err := time.Parse(time.DateOnly, date); err != nil {
		writeProblem(c, router.BadRequest("date must be YYYY-MM-DD"))
		return
	}

	day, err := s.bookings.Day(c.Ctx(), date)
	if err != nil {
		logger.ErrorContext(c.Ctx(), "front desk failed", "err", err)
		writeProblem(c, router.InternalServerError("could not read the day"))
		return
	}
	c.JSON(http.StatusOK, day)
}

func (s *Server) checkIn(c *router.Context, p bookingIDParams) {
	actor, ok := staffActor(c)
	if !ok {
		return
	}
	summary, err := s.bookings.CheckIn(c.Ctx(), p.ID, actor)
	s.writeTransition(c, summary, err)
}

func (s *Server) checkOut(c *router.Context, p bookingIDParams) {
	actor, ok := staffActor(c)
	if !ok {
		return
	}
	summary, err := s.bookings.CheckOut(c.Ctx(), p.ID, actor)
	s.writeTransition(c, summary, err)
}

func (s *Server) cancelBooking(c *router.Context, p bookingIDParams) {
	actor, ok := staffActor(c)
	if !ok {
		return
	}

	var req CancelRequest
	if err := decodeJSON(c, &req); err != nil {
		writeProblem(c, router.BadRequest(err.Error()))
		return
	}
	if req.Reason == "" {
		writeProblem(c, router.BadRequest("a reason is required"))
		return
	}

	summary, err := s.bookings.Cancel(c.Ctx(), p.ID, actor, req.Reason)
	s.writeTransition(c, summary, err)
}

func (s *Server) reassignBooking(c *router.Context, p bookingIDParams) {
	actor, ok := staffActor(c)
	if !ok {
		return
	}

	var req ReassignRequest
	if err := decodeJSON(c, &req); err != nil {
		writeProblem(c, router.BadRequest(err.Error()))
		return
	}
	if req.UnitID == "" {
		writeProblem(c, router.BadRequest("unit_id is required"))
		return
	}

	summary, err := s.bookings.Reassign(c.Ctx(), p.ID, req.UnitID, actor)
	if errors.Is(err, booking.ErrNoUnitAvailable) {
		writeProblem(c, router.Conflict(
			"that pitch is occupied for those dates"))
		return
	}
	s.writeTransition(c, summary, err)
}

// writeTransition is the one place a state change turns into a response, so
// every transition answers "no such booking" and "not in that state" the same
// way.
func (s *Server) writeTransition(
	c *router.Context,
	summary booking.Summary,
	err error,
) {
	switch {
	case err == nil:
		// Cancelling and checking out both free a pitch; checking in does not,
		// but it costs nothing to be consistent and a special case here would
		// be a stale count waiting to happen.
		s.invalidateAvailability()
		c.JSON(http.StatusOK, summary)
	case errors.Is(err, booking.ErrNotFound):
		writeProblem(c, router.NotFound("no such booking"))
	case errors.Is(err, booking.ErrWrongState):
		writeProblem(c, router.Conflict(
			"the booking is not in a state that allows that"))
	default:
		logger.ErrorContext(c.Ctx(), "booking transition failed", "err", err)
		writeProblem(c, router.InternalServerError(
			"could not change the booking"))
	}
}

// staffActor is the signed-in user, which every transition records. A missing
// principal on a route that declared the staff scheme would mean the middleware
// order had been changed, so it is a 500 rather than a silent anonymous write.
func staffActor(c *router.Context) (string, bool) {
	p, ok := auth.PrincipalFor[staffPrincipal](c.Ctx(), staffScheme)
	if !ok {
		logger.ErrorContext(c.Ctx(), "staff route reached with no principal")
		writeProblem(c, router.InternalServerError("could not identify you"))
		return "", false
	}
	return p.UserID, true
}
