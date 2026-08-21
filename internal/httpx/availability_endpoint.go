package httpx

import (
	"errors"
	"net/http"
	"time"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/availability"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/tenant"
	"github.com/joakimcarlsson/minmux/openapi"
	"github.com/joakimcarlsson/minmux/outputcache"
	"github.com/joakimcarlsson/minmux/router"
)

// availabilityCacheTTL is short because inventory moves. A stale "free: 3" that
// is really zero turns into a failed booking at the payment step, which is a
// worse experience than a slightly slower search.
const availabilityCacheTTL = 30 * time.Second

// availabilityCacheTag is what an occupancy write invalidates. The TTL above is
// only a backstop for changes this process did not serve — a hold expiring on
// the sweeper, say — because a count that is stale in the other direction
// offers a pitch somebody has just taken, and the guest finds out at the last
// step.
const availabilityCacheTag = "availability"

type availabilityParams struct {
	Arrival        string `query:"arrival"         desc:"First night, YYYY-MM-DD"`
	Departure      string `query:"departure"       desc:"Exclusive, YYYY-MM-DD"`
	Adults         int    `query:"adults"          desc:"Adults (default 2)"`
	Children       int    `query:"children"        desc:"Children in the party"`
	Pets           int    `query:"pets"            desc:"Filters to pet-friendly"`
	ElectricityAmp int    `query:"electricity_amp" desc:"Minimum amperage"`
	Accessible     bool   `query:"accessible"      desc:"Step-free access only"`
}

// AvailabilityResponse is what a category search returns. It reports counts,
// never unit numbers: the guest buys a category and the pitch is chosen for
// them at booking, so naming one here would be a promise the system does not
// make.
type AvailabilityResponse struct {
	Arrival    string                       `json:"arrival"`
	Departure  string                       `json:"departure"`
	Nights     int                          `json:"nights"`
	Categories []availability.CategoryOffer `json:"categories"`
}

func (s *Server) registerAvailability() {
	s.router.Get("/api/v1/availability", s.availability,
		openapi.Summary("What is free for a stay"),
		openapi.Description(
			"The operator is resolved from the request hostname. Returns one "+
				"entry per category that can host the whole stay, with how "+
				"many of its units are free. Cached briefly, keyed by operator.",
		),
		openapi.Tags("Availability"),
		openapi.NoSecurity(),
		// Every parameter that changes the answer, or the cache serves one
		// search's result for another's.
		tenant.Cached(availabilityCacheTTL, []string{
			"arrival", "departure", "adults", "children", "pets",
			"electricity_amp", "accessible",
		}, outputcache.Tags(availabilityCacheTag)),
		openapi.ReturnsBody[AvailabilityResponse](http.StatusOK, "Offers"),
		openapi.ReturnsBody[router.ProblemDetails](
			http.StatusBadRequest,
			"Bad dates, or the hostname names no operator",
		),
	)
}

func (s *Server) availability(c *router.Context, p availabilityParams) {
	arrival, departure, err := parseStay(p.Arrival, p.Departure)
	if err != nil {
		writeProblem(c, router.BadRequest(err.Error()))
		return
	}

	adults := p.Adults
	if adults == 0 {
		adults = 2
	}

	offers, err := s.avail.Search(c.Ctx(), availability.Query{
		Arrival:        p.Arrival,
		Departure:      p.Departure,
		Adults:         adults,
		Children:       p.Children,
		Pets:           p.Pets,
		ElectricityAmp: p.ElectricityAmp,
		Accessible:     p.Accessible,
	})
	if err != nil {
		if isNoTenant(err) {
			writeProblem(c, router.BadRequest("no operator selected"))
			return
		}
		logger.ErrorContext(c.Ctx(), "availability search failed", "err", err)
		writeProblem(
			c,
			router.InternalServerError("could not search availability"),
		)
		return
	}

	c.JSON(http.StatusOK, AvailabilityResponse{
		Arrival:    p.Arrival,
		Departure:  p.Departure,
		Nights:     int(departure.Sub(arrival).Hours() / 24),
		Categories: offers,
	})
}

// parseStay validates the date pair. Departure is exclusive, so a same-day
// arrival and departure is zero nights and cannot be sold — rejected here
// rather than surfacing later as an empty result nobody can explain.
func parseStay(arrival, departure string) (time.Time, time.Time, error) {
	a, err := time.Parse(time.DateOnly, arrival)
	if err != nil {
		return time.Time{}, time.Time{}, errors.New(
			"arrival must be a date, YYYY-MM-DD")
	}
	d, err := time.Parse(time.DateOnly, departure)
	if err != nil {
		return time.Time{}, time.Time{}, errors.New(
			"departure must be a date, YYYY-MM-DD")
	}
	if !d.After(a) {
		return time.Time{}, time.Time{}, errors.New(
			"departure must be after arrival; it is the day the guest leaves, " +
				"not the last night",
		)
	}
	return a, d, nil
}
