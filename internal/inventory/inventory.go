// Package inventory owns what an operator has to sell: its sites, the
// categories guests choose from, and the physical units those categories are
// made of. It also owns maintenance blocks, because a block is an occupancy row
// like any other.
//
// It stops at availability. What is free on a given night depends on
// allocations across bookings and holds, and that question belongs to the
// availability package.
package inventory

import "errors"

// ErrOverlap is returned when an allocation would collide with an existing one
// on the same unit. It is a translation of the exclusion constraint, not a
// check performed beforehand: the database is the authority, and a Go-side
// pre-check would be a second implementation free to disagree with it.
var ErrOverlap = errors.New(
	"inventory: unit is already occupied for those dates",
)

// ErrNotFound is returned when no row matches the lookup.
var ErrNotFound = errors.New("inventory: not found")

// Site is one physical location. Statistics reporting, VAT and the guest
// registration duty all resolve at site rather than operator, because one
// company running three campsites in two municipalities is the normal case.
type Site struct {
	ID           string `json:"id"`
	Name         string `json:"name"           desc:"Display name"`
	Slug         string `json:"slug"           desc:"Stable external key"`
	Municipality string `json:"municipality"   desc:"Reporting municipality"`
	Country      string `json:"country"        desc:"ISO 3166-1 alpha-2"`
	Timezone     string `json:"timezone"`
	CheckInTime  string `json:"check_in_time"`
	CheckOutTime string `json:"check_out_time"`
}

// Category is what a guest actually buys. Kind is tomt, stuga, villavagn,
// glamping or rum. The physical unit is chosen by the
// system, not by the guest, and is not shown until check-in.
type Category struct {
	ID                string `json:"id"`
	SiteID            string `json:"site_id"`
	Code              string `json:"code"                desc:"Stable key"`
	Name              string `json:"name"`
	Kind              string `json:"kind"                desc:"tomt|stuga|rum"`
	RevenueClass      string `json:"revenue_class"       desc:"pitch|lodging"`
	MaxOccupancy      int    `json:"max_occupancy"`
	MinElectricityAmp *int   `json:"min_electricity_amp"`
	PetsAllowed       bool   `json:"pets_allowed"`
	Accessible        bool   `json:"accessible"`
	Sanitary          bool   `json:"sanitary"`
	SortOrder         int    `json:"sort_order"`
	Units             int    `json:"units"               desc:"Active units here"`
}

// Unit is one physical pitch, cabin or room.
//
// Surface is grass, gravel, hardstanding, sand or other; shade is sun, partial
// or shade; cleanliness is clean, dirty or ready. The tags carry a short form
// so the generated spec stays readable at the column limit.
//
// The attributes here are the ones availability filters on and the assigner
// scores on. Anything else belongs in unit_amenity: a range test such as
// electricity_amp >= 10 cannot be served from a tag table.
type Unit struct {
	ID                string   `json:"id"`
	SiteID            string   `json:"site_id"`
	CategoryID        string   `json:"category_id"`
	CategoryCode      string   `json:"category_code"`
	Code              string   `json:"code"`
	Status            string   `json:"status"               desc:"active|retired"`
	ElectricityAmp    *int     `json:"electricity_amp"`
	AreaM2            *int     `json:"area_m2"`
	MaxVehicleLengthM *float64 `json:"max_vehicle_length_m"`
	MaxOccupancy      int      `json:"max_occupancy"`
	PetsAllowed       bool     `json:"pets_allowed"`
	Accessible        bool     `json:"accessible"`
	HasWater          bool     `json:"has_water"`
	HasGreywater      bool     `json:"has_greywater"`
	HasSewer          bool     `json:"has_sewer"`
	Surface           *string  `json:"surface"              desc:"grass|gravel"`
	Shade             *string  `json:"shade"                desc:"sun|partial"`
	DriveThrough      bool     `json:"drive_through"`
	Sanitary          bool     `json:"sanitary"`
	View              *string  `json:"view"`
	Cleanliness       string   `json:"cleanliness"          desc:"clean|dirty"`
	SortOrder         int      `json:"sort_order"`
}

// Allocation is one occupancy row as the tape chart sees it. Bookings, holds
// and blocks share the shape, which is what lets one constraint keep all three
// from overlapping.
type Allocation struct {
	ID          string  `json:"id"`
	UnitID      string  `json:"unit_id"`
	Kind        string  `json:"kind"         desc:"booking|hold|block"`
	State       string  `json:"state"`
	Arrival     string  `json:"arrival"      desc:"First night, inclusive"`
	Departure   string  `json:"departure"    desc:"Departure day, exclusive"`
	BlockReason *string `json:"block_reason"`
	UnitPinned  bool    `json:"unit_pinned"`
}

// CalendarRow is one unit and everything occupying it inside the requested
// window.
type CalendarRow struct {
	Unit        Unit         `json:"unit"`
	Allocations []Allocation `json:"allocations"`
}

// NewBlock describes a maintenance block to create.
type NewBlock struct {
	UnitID    string
	Arrival   string
	Departure string
	Reason    string
}
