package httpx

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/pricing"
	"github.com/joakimcarlsson/minmux/openapi"
	"github.com/joakimcarlsson/minmux/router"
)

// QuoteRequest is what the guest chose. Children carry a birth date rather than
// a count, because Nordic sites price 0-3, 4-12 and 13-15 apart and a count
// cannot express that.
type QuoteRequest struct {
	SiteID       string       `json:"site_id" desc:"Needed if code spans sites"`
	CategoryCode string       `json:"category_code"`
	Arrival      string       `json:"arrival"       desc:"First night, YYYY-MM-DD"`
	Departure    string       `json:"departure"     desc:"Exclusive, YYYY-MM-DD"`
	Adults       int          `json:"adults"`
	Children     []QuoteChild `json:"children"`
	Pets         int          `json:"pets"`
	Vehicles     int          `json:"vehicles"`
	CampaignCode string       `json:"campaign_code"`
}

// QuoteChild is one child on the booking.
type QuoteChild struct {
	DateOfBirth string `json:"date_of_birth" desc:"YYYY-MM-DD"`
}

func (s *Server) registerQuotes() {
	s.router.Post(
		"/api/v1/quotes",
		s.createQuote,
		openapi.Summary("Price a stay"),
		openapi.Description(
			"Returns a full breakdown with per-line VAT and an ordered explain "+
				"trace, and stores it. The quote id is what a booking is "+
				"confirmed against, so the price a guest saw is the price they "+
				"are charged. A stay that violates a rate rule is refused with "+
				"a reason rather than silently repriced.",
		),
		openapi.Tags("Pricing"),
		// Guests reach this anonymously and the operator comes from the
		// hostname. Staff reach it from the dashboard, which runs on a plain
		// host with no operator label — so a staff token is accepted and pins
		// the operator from the session instead. Declared in this order: a
		// valid staff token is used, its absence falls through to anonymous
		// rather than 401.
		openapi.Security(staffScheme),
		openapi.OptionalSecurity(),
		openapi.ReturnsBody[pricing.StoredQuote](
			http.StatusCreated,
			"The quote",
		),
		openapi.ReturnsBody[router.ProblemDetails](
			http.StatusUnprocessableEntity, "The stay is not sellable"),
		openapi.ReturnsBody[router.ProblemDetails](
			http.StatusBadRequest, "Bad dates, unknown category or campaign"),
	)
}

// Bounds on a booking request. These are not arbitrary: a party larger than
// maxParty cannot fit any category bokarn models, and unbounded counts multiply
// into an int64 overflow that turns a price negative — found by testing,
// not by reading the code.
const (
	maxParty         = 60
	maxPets          = 20
	maxVehicles      = 20
	maxStayNights    = 365
	oldestGuestYears = 120
)

func (s *Server) createQuote(c *router.Context) {
	var req QuoteRequest
	if err := decodeJSON(c, &req); err != nil {
		writeProblem(c, router.BadRequest(err.Error()))
		return
	}
	if req.CategoryCode == "" {
		writeProblem(c, router.BadRequest("category_code is required"))
		return
	}

	arrival, departure, err := parseStay(req.Arrival, req.Departure)
	if err != nil {
		writeProblem(c, router.BadRequest(err.Error()))
		return
	}

	today := time.Now().Truncate(24 * time.Hour)
	if arrival.Before(today.AddDate(0, 0, -1)) {
		writeProblem(c, router.BadRequest("arrival is in the past"))
		return
	}
	if nights := int(departure.Sub(arrival).Hours() / 24); nights > maxStayNights {
		writeProblem(c, router.BadRequest(fmt.Sprintf(
			"a stay may be at most %d nights", maxStayNights)))
		return
	}

	if req.Adults < 1 {
		writeProblem(c, router.BadRequest("at least one adult is required"))
		return
	}
	if req.Pets < 0 || req.Vehicles < 0 {
		writeProblem(c, router.BadRequest(
			"pets and vehicles cannot be negative"))
		return
	}
	if req.Adults > maxParty || len(req.Children) > maxParty ||
		req.Adults+len(req.Children) > maxParty {
		writeProblem(c, router.BadRequest(fmt.Sprintf(
			"a party may be at most %d guests", maxParty)))
		return
	}
	if req.Pets > maxPets || req.Vehicles > maxVehicles {
		writeProblem(c, router.BadRequest("too many pets or vehicles"))
		return
	}

	children := make([]pricing.Guest, 0, len(req.Children))
	for _, ch := range req.Children {
		dob, err := time.Parse(time.DateOnly, ch.DateOfBirth)
		if err != nil {
			writeProblem(c, router.BadRequest(
				"every child needs a date of birth, YYYY-MM-DD"))
			return
		}
		// A birth date after arrival, or implausibly long before it, is a typo.
		// Accepting it silently reclassifies the guest as an adult and charges
		// them accordingly.
		oldest := arrival.AddDate(-oldestGuestYears, 0, 0)
		if dob.After(arrival) || dob.Before(oldest) {
			writeProblem(c, router.BadRequest(
				"a child's date of birth must be before arrival and plausible"))
			return
		}
		children = append(children, pricing.Guest{DateOfBirth: ch.DateOfBirth})
	}

	quote, err := s.pricing.CreateQuote(c.Ctx(), req.SiteID, req.CategoryCode,
		pricing.Request{
			Arrival:      req.Arrival,
			Departure:    req.Departure,
			Adults:       req.Adults,
			Children:     children,
			Pets:         req.Pets,
			Vehicles:     req.Vehicles,
			CampaignCode: req.CampaignCode,
		}, time.Now())

	switch {
	case err == nil:
		c.JSON(http.StatusCreated, quote)

	case isNotSellable(err):
		var reject pricing.ErrStayNotSellable
		errors.As(err, &reject)
		pd := &router.ProblemDetails{
			Status: http.StatusUnprocessableEntity,
			Title:  "stay not sellable",
			Type:   reject.Reason,
			Detail: reject.Detail,
		}
		writeProblem(c, pd)

	case errors.Is(err, pricing.ErrNoRatePlan):
		writeProblem(c, router.BadRequest("unknown category"))

	case errors.Is(err, pricing.ErrAmbiguousCategory):
		writeProblem(c, router.BadRequest(
			"that category exists at several of this operator's sites; "+
				"send site_id to say which"))

	case errors.Is(err, pricing.ErrUnknownCampaign):
		writeProblem(c, router.BadRequest("unknown or expired campaign code"))

	case errors.Is(err, pricing.ErrNoRate):
		// An uncompiled calendar is an operator configuration error, not
		// something the guest can fix, so it reads as unavailability rather
		// than as their mistake — and it is logged as the fault it is.
		logger.ErrorContext(c.Ctx(), "rate calendar has a gap", "err", err)
		writeProblem(c, &router.ProblemDetails{
			Status: http.StatusUnprocessableEntity,
			Title:  "stay not sellable",
			Type:   "no_rate",
			Detail: "those dates are not on sale",
		})

	case isNoTenant(err):
		writeProblem(c, router.BadRequest("no operator selected"))

	default:
		logger.ErrorContext(c.Ctx(), "quote failed", "err", err)
		writeProblem(c, router.InternalServerError("could not price the stay"))
	}
}

func isNotSellable(err error) bool {
	var reject pricing.ErrStayNotSellable
	return errors.As(err, &reject)
}
