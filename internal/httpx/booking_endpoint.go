package httpx

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/booking"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/pricing"
	"github.com/joakimcarlsson/minmux/openapi"
	"github.com/joakimcarlsson/minmux/router"
)

// HoldRequest reserves a pitch while the guest finishes checking out.
type HoldRequest struct {
	CategoryCode   string `json:"category_code"`
	Arrival        string `json:"arrival"         desc:"First night, YYYY-MM-DD"`
	Departure      string `json:"departure"       desc:"Exclusive, YYYY-MM-DD"`
	Adults         int    `json:"adults"`
	Children       int    `json:"children"`
	Pets           int    `json:"pets"`
	ElectricityAmp int    `json:"electricity_amp"`
	Accessible     bool   `json:"accessible"`
}

// BookingRequest confirms a quote.
//
// The stay is repeated here rather than taken from the quote, and it has to be:
// the server hashes what this says and refuses if it differs from what was
// priced. Sending only a quote id would leave nothing to check the quote
// against.
type BookingRequest struct {
	QuoteID   string `json:"quote_id"`
	HoldToken string `json:"hold_token"`

	CategoryCode string       `json:"category_code"`
	Arrival      string       `json:"arrival"`
	Departure    string       `json:"departure"`
	Adults       int          `json:"adults"`
	Children     []QuoteChild `json:"children"`
	Pets         int          `json:"pets"`
	Vehicles     int          `json:"vehicles"`
	CampaignCode string       `json:"campaign_code"`

	ElectricityAmp int  `json:"electricity_amp"`
	Accessible     bool `json:"accessible"`

	Guest            BookingGuest `json:"guest"`
	Locale           string       `json:"locale"            desc:"sv, en or de"`
	MarketingConsent bool         `json:"marketing_consent"`
	Notes            string       `json:"notes"`
}

// BookingGuest is who the booking is for.
type BookingGuest struct {
	GivenNames         string `json:"given_names"`
	Surname            string `json:"surname"`
	Email              string `json:"email"`
	Phone              string `json:"phone"`
	CountryOfResidence string `json:"country_of_residence" desc:"ISO 3166 alpha-2"`
}

type holdParams struct {
	Token string `path:"token" desc:"The hold token"`
}

type bookingRefParams struct {
	Reference string `path:"reference" desc:"Booking reference"`
	Token     string `                 desc:"Token from the email" query:"token"`
}

const idempotencyNote = "Requires an Idempotency-Key header. A repeat of the " +
	"same key returns the original result with 200 rather than creating a " +
	"second one — a guest double-tapping Boka on a weak connection is the " +
	"real duplicate-booking failure mode."

func (s *Server) registerBookings() {
	s.router.Post("/api/v1/holds", s.createHold,
		openapi.Summary("Reserve a pitch for a few minutes"),
		openapi.Description(
			"The operator comes from the hostname. A physical unit is assigned "+
				"immediately and never shown to the guest: the exclusion "+
				"constraint on occupancy is then the only concurrency "+
				"authority, so two guests racing for the last cabin get "+
				"exactly one 201 and one 409. "+idempotencyNote,
		),
		openapi.Tags("Booking"),
		openapi.NoSecurity(),
		openapi.ReturnsBody[booking.Hold](http.StatusCreated, "The hold"),
		openapi.ReturnsBody[router.ProblemDetails](
			http.StatusConflict, "Nothing free in that category"),
		openapi.ReturnsBody[router.ProblemDetails](
			http.StatusBadRequest, "Bad dates, party or category"),
	)

	s.router.Delete("/api/v1/holds/{token}", s.releaseHold,
		openapi.Summary("Give a held pitch back"),
		openapi.Description(
			"Called when a guest abandons checkout, so the pitch returns to "+
				"inventory without waiting for the sweeper. Idempotent: a hold "+
				"that has already expired is not an error.",
		),
		openapi.Tags("Booking"),
		openapi.NoSecurity(),
		openapi.Returns(http.StatusNoContent, "Released"),
	)

	s.router.Post("/api/v1/bookings", s.createBooking,
		openapi.Summary("Confirm a quote into a booking"),
		openapi.Description(
			"Copies the quoted breakdown onto the booking verbatim and never "+
				"prices it again. A quote that has expired, or a request "+
				"describing a different stay than was priced, is refused with "+
				"409 rather than repriced: a guest who saw one total must be "+
				"told it moved, not charged the new one. "+idempotencyNote,
		),
		openapi.Tags("Booking"),
		openapi.NoSecurity(),
		openapi.ReturnsBody[booking.Confirmed](
			http.StatusCreated, "Confirmed"),
		openapi.ReturnsBody[booking.Confirmed](
			http.StatusOK, "Already confirmed under this idempotency key"),
		openapi.ReturnsBody[router.ProblemDetails](
			http.StatusConflict,
			"The quote expired, the stay changed, or the pitch went",
		),
		openapi.ReturnsBody[router.ProblemDetails](
			http.StatusBadRequest, "Bad guest details, party or quote"),
	)

	s.router.Get("/api/v1/bookings/{reference}", s.readBooking,
		openapi.Summary("Read a booking with the token from its email"),
		openapi.Description(
			"Both the reference and the token are required, and the token is "+
				"stored only as a hash. The reference alone is meant to be "+
				"quotable over the phone and is not a credential.",
		),
		openapi.Tags("Booking"),
		openapi.NoSecurity(),
		openapi.ReturnsBody[booking.Detail](http.StatusOK, "The booking"),
		openapi.ReturnsBody[router.ProblemDetails](
			http.StatusNotFound,
			"No such booking, or the token does not match",
		),
	)
}

func (s *Server) createHold(c *router.Context) {
	if _, ok := idempotencyKey(c); !ok {
		return
	}

	var req HoldRequest
	if err := decodeJSON(c, &req); err != nil {
		writeProblem(c, router.BadRequest(err.Error()))
		return
	}
	if req.CategoryCode == "" {
		writeProblem(c, router.BadRequest("category_code is required"))
		return
	}
	if err := validateStayWindow(req.Arrival, req.Departure); err != nil {
		writeProblem(c, router.BadRequest(err.Error()))
		return
	}
	if err := validateParty(
		req.Adults, req.Children, req.Pets, 0,
	); err != nil {
		writeProblem(c, router.BadRequest(err.Error()))
		return
	}

	hold, err := s.bookings.Hold(c.Ctx(), booking.HoldRequest{
		CategoryCode:   req.CategoryCode,
		Arrival:        req.Arrival,
		Departure:      req.Departure,
		Adults:         req.Adults,
		Children:       req.Children,
		Pets:           req.Pets,
		ElectricityAmp: req.ElectricityAmp,
		Accessible:     req.Accessible,
	}, time.Now())

	switch {
	case err == nil:
		// The hold changed occupancy, so a cached availability answer is now
		// wrong in the direction that matters: it would offer a pitch that has
		// just been taken.
		s.invalidateAvailability()
		c.JSON(http.StatusCreated, hold)
	case errors.Is(err, booking.ErrNoUnitAvailable):
		writeProblem(c, &router.ProblemDetails{
			Status: http.StatusConflict,
			Title:  "nothing available",
			Type:   "no_unit_available",
			Detail: "no pitch in that category can host the whole stay",
		})
	case isBadCategory(err):
		writeProblem(c, router.BadRequest("unknown category"))
	case isNoTenant(err):
		writeProblem(c, router.BadRequest("no operator selected"))
	default:
		logger.ErrorContext(c.Ctx(), "hold failed", "err", err)
		writeProblem(c, router.InternalServerError("could not hold a pitch"))
	}
}

func (s *Server) releaseHold(c *router.Context, p holdParams) {
	if err := s.bookings.ReleaseHold(c.Ctx(), p.Token); err != nil {
		logger.ErrorContext(c.Ctx(), "release hold failed", "err", err)
		writeProblem(
			c,
			router.InternalServerError("could not release the hold"),
		)
		return
	}
	s.invalidateAvailability()
	c.NoContent()
}

func (s *Server) createBooking(c *router.Context) {
	key, ok := idempotencyKey(c)
	if !ok {
		return
	}

	var req BookingRequest
	if err := decodeJSON(c, &req); err != nil {
		writeProblem(c, router.BadRequest(err.Error()))
		return
	}

	confirm, problem := s.toConfirmRequest(req, key)
	if problem != nil {
		writeProblem(c, problem)
		return
	}

	result, err := s.bookings.Confirm(c.Ctx(), confirm, time.Now())
	switch {
	case err == nil && result.Replayed:
		c.JSON(http.StatusOK, result)
	case err == nil:
		s.invalidateAvailability()
		c.JSON(http.StatusCreated, result)

	case errors.Is(err, pricing.ErrQuoteNotFound):
		writeProblem(c, router.BadRequest("unknown quote"))

	case errors.Is(err, booking.ErrQuoteExpired):
		writeProblem(c, &router.ProblemDetails{
			Status: http.StatusConflict,
			Title:  "the price has expired",
			Type:   "quote_expired",
			Detail: "ask for a fresh price for those dates",
		})

	case errors.Is(err, booking.ErrQuoteMismatch):
		writeProblem(c, &router.ProblemDetails{
			Status: http.StatusConflict,
			Title:  "the stay changed",
			Type:   "quote_mismatch",
			Detail: "the party or the dates differ from what was priced",
		})

	case errors.Is(err, booking.ErrHoldExpired),
		errors.Is(err, booking.ErrNoUnitAvailable):
		writeProblem(c, &router.ProblemDetails{
			Status: http.StatusConflict,
			Title:  "the pitch is gone",
			Type:   "hold_expired",
			Detail: "the pitch held for you was taken; please search again",
		})

	case isNoTenant(err):
		writeProblem(c, router.BadRequest("no operator selected"))

	default:
		logger.ErrorContext(c.Ctx(), "confirm failed", "err", err)
		writeProblem(
			c, router.InternalServerError("could not confirm the booking"))
	}
}

// toConfirmRequest validates and converts. It returns a ProblemDetails rather
// than an error so each rejection can carry its own wording — "at least one
// adult" and "that is not a country code" are different mistakes.
func (s *Server) toConfirmRequest(
	req BookingRequest,
	key string,
) (booking.ConfirmRequest, *router.ProblemDetails) {
	if req.QuoteID == "" {
		return booking.ConfirmRequest{},
			router.BadRequest("quote_id is required")
	}
	if err := validateStayWindow(req.Arrival, req.Departure); err != nil {
		return booking.ConfirmRequest{}, router.BadRequest(err.Error())
	}
	if err := validateParty(
		req.Adults, len(req.Children), req.Pets, req.Vehicles,
	); err != nil {
		return booking.ConfirmRequest{}, router.BadRequest(err.Error())
	}

	children := make([]string, 0, len(req.Children))
	for _, ch := range req.Children {
		if _, err := time.Parse(time.DateOnly, ch.DateOfBirth); err != nil {
			return booking.ConfirmRequest{}, router.BadRequest(
				"every child needs a date of birth, YYYY-MM-DD")
		}
		children = append(children, ch.DateOfBirth)
	}

	g := req.Guest
	switch {
	case strings.TrimSpace(g.GivenNames) == "",
		strings.TrimSpace(g.Surname) == "":
		return booking.ConfirmRequest{},
			router.BadRequest("a first name and a surname are required")
	case !plausibleEmail(g.Email):
		return booking.ConfirmRequest{},
			router.BadRequest("a valid email address is required")
	case strings.TrimSpace(g.Phone) == "":
		return booking.ConfirmRequest{},
			router.BadRequest("a phone number is required")
	case len(g.CountryOfResidence) != 2:
		// Not defaulted to SE. Country of residence is the statistics key and a
		// guessed one is a wrong figure in a public report.
		return booking.ConfirmRequest{}, router.BadRequest(
			"country_of_residence is required, as a two-letter code")
	}

	locale := req.Locale
	switch locale {
	case "sv", "en", "de":
	default:
		return booking.ConfirmRequest{}, router.BadRequest(
			"locale must be sv, en or de")
	}

	return booking.ConfirmRequest{
		QuoteID:        req.QuoteID,
		HoldToken:      req.HoldToken,
		IdempotencyKey: key,
		CategoryCode:   req.CategoryCode,
		Arrival:        req.Arrival,
		Departure:      req.Departure,
		Adults:         req.Adults,
		Children:       children,
		Pets:           req.Pets,
		Vehicles:       req.Vehicles,
		CampaignCode:   req.CampaignCode,
		ElectricityAmp: req.ElectricityAmp,
		Accessible:     req.Accessible,
		Guest: booking.GuestDetails{
			GivenNames:         strings.TrimSpace(g.GivenNames),
			Surname:            strings.TrimSpace(g.Surname),
			Email:              strings.TrimSpace(g.Email),
			Phone:              strings.TrimSpace(g.Phone),
			CountryOfResidence: strings.ToUpper(g.CountryOfResidence),
		},
		Locale:           locale,
		Channel:          "web",
		MarketingConsent: req.MarketingConsent,
		Notes:            strings.TrimSpace(req.Notes),
	}, nil
}

func (s *Server) readBooking(c *router.Context, p bookingRefParams) {
	if p.Token == "" {
		// Deliberately the same answer as a wrong token: telling an anonymous
		// caller that a reference exists but needs a token confirms the
		// reference, which is guessable.
		writeProblem(c, router.NotFound("no such booking"))
		return
	}

	detail, err := s.bookings.ByReference(c.Ctx(), p.Reference, p.Token)
	switch {
	case err == nil:
		c.JSON(http.StatusOK, detail)
	case errors.Is(err, booking.ErrNotFound):
		writeProblem(c, router.NotFound("no such booking"))
	case isNoTenant(err):
		writeProblem(c, router.BadRequest("no operator selected"))
	default:
		logger.ErrorContext(c.Ctx(), "read booking failed", "err", err)
		writeProblem(
			c,
			router.InternalServerError("could not read the booking"),
		)
	}
}

// idempotencyKey reads the header and writes the problem itself when it is
// missing, so every caller is one `if !ok { return }`.
func idempotencyKey(c *router.Context) (string, bool) {
	key := strings.TrimSpace(c.Request.Header.Get("Idempotency-Key"))
	if key == "" {
		writeProblem(c, router.BadRequest(
			"an Idempotency-Key header is required"))
		return "", false
	}
	if len(key) > 200 {
		writeProblem(c, router.BadRequest("Idempotency-Key is too long"))
		return "", false
	}
	return key, true
}

// invalidateAvailability drops every cached availability answer.
//
// Cache tags in minmux are fixed per route rather than per request, so this
// clears the route for every operator at once. That is accepted rather than
// worked around: occupancy changes are rare next to searches, and the
// alternative — a stale count that offers a pitch somebody just took — turns
// into a failed booking at the last step.
func (s *Server) invalidateAvailability() {
	s.cache.InvalidateTag(availabilityCacheTag)
}

func isBadCategory(err error) bool {
	return err != nil &&
		strings.Contains(err.Error(), "resolve category")
}

// plausibleEmail is a shape check, not validation. Whether an address exists is
// answered by whether the confirmation arrives, and a stricter regex here would
// reject valid addresses for no gain.
func plausibleEmail(address string) bool {
	at := strings.IndexByte(address, '@')
	if at < 1 || at == len(address)-1 {
		return false
	}
	return strings.Contains(address[at+1:], ".") &&
		!strings.ContainsAny(address, " \t\r\n")
}
