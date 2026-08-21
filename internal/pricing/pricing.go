// Package pricing turns a stay into a price.
//
// The engine is a pure function — no database handle, no clock, no map
// iteration, no randomness. The store loads a snapshot, the engine computes
// from it. That separation is what makes the golden fixtures in testdata/ a
// specification rather than a regression net: a revenue manager can file a
// pricing bug as a JSON file.
//
// Nothing here falls back. A date with no compiled rate, a VAT code that does
// not resolve, an age that matches no band above the child bands — each is an
// error naming what was missing, because a price that silently omits a night is
// worse than no price at all.
package pricing

import (
	"errors"
	"fmt"
)

// EngineVersion is stamped onto every quote and every booking priced by it. A
// booking records the version that produced it so a later change to this file
// can never be mistaken for how an old booking was calculated.
const EngineVersion = 1

// Line kinds, in the order the pipeline emits them.
const (
	KindAccommodation = "accommodation"
	KindExtraPerson   = "extra_person"
	KindPet           = "pet"
	KindVehicle       = "vehicle"
	KindDerived       = "derived_plan"
	KindAdjuster      = "adjuster"
	KindLOSDiscount   = "los_discount"
	KindCampaign      = "campaign"
	KindRounding      = "rounding"
)

// ErrStayNotSellable is returned when the stay violates a rule on the rate
// calendar. It carries a machine-readable reason so the guest site can say
// which rule, rather than showing an empty result the guest cannot act on.
type ErrStayNotSellable struct {
	Reason string
	Detail string
}

func (e ErrStayNotSellable) Error() string {
	return fmt.Sprintf(
		"pricing: stay not sellable (%s): %s",
		e.Reason,
		e.Detail,
	)
}

// Reasons a stay can be refused.
const (
	ReasonClosed            = "closed"
	ReasonOverCapacity      = "over_capacity"
	ReasonPetsNotAllowed    = "pets_not_allowed"
	ReasonMinStay           = "min_stay"
	ReasonMaxStay           = "max_stay"
	ReasonArrivalDay        = "arrival_day"
	ReasonClosedToArrival   = "closed_to_arrival"
	ReasonClosedToDeparture = "closed_to_departure"
)

// ErrNoRate is returned when a night in the stay has no compiled rate. It is
// never treated as free: an uncompiled calendar is a configuration error and
// must be visible as one.
var ErrNoRate = errors.New("pricing: no rate compiled for a night in the stay")

// ErrQuoteNotFound is returned when a quote id does not resolve. A quote that
// has expired still resolves: the confirm path needs to tell a guest that their
// price moved, which it cannot do if an expired quote looks like a typo.
var ErrQuoteNotFound = errors.New("pricing: quote not found")

// ErrNoVATCode is returned when a line's VAT code does not resolve on the stay
// date. VAT codes are date-effective, so this usually means a rate outlived its
// code's validity window.
var ErrNoVATCode = errors.New("pricing: vat code does not resolve")

// Guest is one person on the booking. Children carry a date of birth because
// Nordic sites price 0-3, 4-12 and 13-15 apart, and a checkbox cannot express
// that.
type Guest struct {
	DateOfBirth string
}

// Request is what the guest asked for.
type Request struct {
	Arrival      string
	Departure    string
	Adults       int
	Children     []Guest
	Pets         int
	Vehicles     int
	CampaignCode string
	// LeadDays is days between the request and arrival, supplied by the caller
	// so the engine stays free of a clock.
	LeadDays int
	// OccupancyBP is how full the category already is, in basis points, also
	// supplied rather than queried.
	OccupancyBP int
}

// Nights is the length of the stay. Departure is exclusive.
func (r Request) Nights() int { return daysBetween(r.Arrival, r.Departure) }

// VATCode is a rate resolved for a date.
type VATCode struct {
	Code      string
	RateBP    int
	Treatment string
}

// AgeBand prices one age range per night.
type AgeBand struct {
	Code               string
	AgeFrom            int
	AgeTo              int
	PricePerNightMinor int64
}

// RateDay is one compiled night on one plan.
type RateDay struct {
	Day               string
	BaseMinor         int64
	IncludedAdults    int
	IncludedChildren  int
	AdultExtraMinor   int64
	ChildExtraMinor   int64
	PetMinor          int64
	VehicleMinor      int64
	MinStay           int
	MaxStay           int
	ArrivalMask       int
	Closed            bool
	ClosedToArrival   bool
	ClosedToDeparture bool
}

// Adjuster is one dynamic-pricing rule. Exactly one of FactorBP and DeltaMinor
// is set, enforced by a CHECK on the table.
type Adjuster struct {
	Name          string
	Priority      int
	OccupancyFrom int
	OccupancyTo   int
	LeadDaysFrom  int
	LeadDaysTo    int
	HasOccupancy  bool
	HasLeadDays   bool
	FactorBP      int
	DeltaMinor    int64
	UsesFactor    bool
}

// Campaign is a discount code. Name is what the operator calls it, and it is
// what a guest sees on their breakdown: a code is an input, not a description.
type Campaign struct {
	Code  string
	Name  string
	Kind  string
	Value int64
}

// RatePlan is the plan being quoted. Name is the operator's wording and is
// what reaches a price breakdown; Code is a stable key for channels and rules,
// and showing it to a guest is showing them the plumbing.
type RatePlan struct {
	ID            string
	Code          string
	Name          string
	Currency      string
	VATCode       string
	DeriveOp      string
	DeriveValueBP int
	MinPriceMinor *int64
	MaxPriceMinor *int64
}

// LOSDiscount is a length-of-stay rule: stay this many nights, get this many
// basis points off the accommodation subtotal.
type LOSDiscount struct {
	MinNights int
	PercentBP int
}

// Snapshot is everything the engine is allowed to see. Assembling it is the
// store's job; the engine never reaches past it.
type Snapshot struct {
	Plan      RatePlan
	Days      map[string]RateDay
	AgeBands  []AgeBand
	Adjusters []Adjuster
	LOS       []LOSDiscount
	Campaign  *Campaign
	VATCodes  map[string]VATCode

	// The category's own limits, so the engine refuses a stay the inventory
	// could never host. Availability already filters on these; without them
	// here, a quote prices a party the campsite has nowhere to put — and a
	// pet fee for a cabin that takes no pets.
	CategoryMaxOccupancy int
	CategoryPetsAllowed  bool
}

// Line is one row of the breakdown. Every line carries its own VAT rate,
// because a discount spanning two rates must be split into two lines or the VAT
// return is wrong.
type Line struct {
	Seq            int    `json:"seq"`
	Kind           string `json:"kind"`
	StayDate       string `json:"stay_date,omitempty"`
	Description    string `json:"description"`
	Qty            int    `json:"qty"`
	UnitGrossMinor int64  `json:"unit_gross_minor"`
	GrossMinor     int64  `json:"gross_minor"`
	NetMinor       int64  `json:"net_minor"`
	VATMinor       int64  `json:"vat_minor"`
	VATCode        string `json:"vat_code"`
	VATRateBP      int    `json:"vat_rate_bp"`
	VATTreatment   string `json:"vat_treatment"`
}

// Totals are the sum of the lines, never computed any other way.
type Totals struct {
	GrossMinor int64            `json:"gross_minor"`
	NetMinor   int64            `json:"net_minor"`
	VATMinor   int64            `json:"vat_minor"`
	Currency   string           `json:"currency"`
	ByRate     map[string]int64 `json:"by_rate"`
}

// Step is one entry in the explain trace. The trace ships to staff as a screen,
// which is what turns "why this price" from a support ticket into a self-serve
// answer.
type Step struct {
	Rule   string `json:"rule"`
	Detail string `json:"detail"`
	Effect int64  `json:"effect_minor"`
}

// Quote is the engine's output.
type Quote struct {
	EngineVersion int    `json:"engine_version"`
	Nights        int    `json:"nights"`
	Lines         []Line `json:"lines"`
	Totals        Totals `json:"totals"`
	Explain       []Step `json:"explain"`
}

// Label is what a guest should see. The name when the operator gave one, the
// code when they did not — never an empty line, which is what a bare name would
// produce on a plan created before the column existed.
func (p RatePlan) Label() string {
	if p.Name != "" {
		return p.Name
	}
	return p.Code
}

// Label is the campaign's wording, falling back to its code for the same
// reason.
func (c Campaign) Label() string {
	if c.Name != "" {
		return c.Name
	}
	return c.Code
}
