// Package guest owns the people a booking is for.
//
// It stops at identity and consent. What a guest owes, what they were charged
// and who to invoice belong to billing, and the registerkort a foreign national
// signs at reception belongs to the front desk — both are separate tables with
// separate retention rules, and merging them here is what makes a guest record
// impossible to erase later.
package guest

import (
	"errors"
	"time"
)

// ErrNotFound is returned when no guest matches.
var ErrNotFound = errors.New("guest: not found")

// RetentionAfterDeparture is how long a guest's personal data is kept once
// their stay is over.
//
// Two seasons plus a month: long enough that a guest returning next summer is
// recognised rather than re-typed, short enough to be defensible as a limited
// purpose. It is a single constant rather than a nullable column so that no
// guest row can exist without an erasure date; per-operator retention arrives
// with the purge job, and this is the default until it does.
const RetentionAfterDeparture = 25 * 30 * 24 * time.Hour

// Identity is a person who books, scoped to one operator. A guest who books at
// two campsites is two rows, because erasure at one must not touch the other.
type Identity struct {
	ID                 string `json:"id"`
	GivenNames         string `json:"given_names"`
	Surname            string `json:"surname"`
	Email              string `json:"email"`
	Phone              string `json:"phone"`
	CountryOfResidence string `json:"country_of_residence"`
	Citizenship        string `json:"citizenship,omitempty"`
}

// Name is how the guest is shown to reception: surname first, because that is
// how an arrivals list is read.
func (i Identity) Name() string {
	if i.GivenNames == "" {
		return i.Surname
	}
	return i.Surname + ", " + i.GivenNames
}

// NewIdentity is what a checkout supplies.
type NewIdentity struct {
	GivenNames         string
	Surname            string
	Email              string
	Phone              string
	CountryOfResidence string
	// PurgeAfter is computed by the caller from the stay it is booking, so this
	// package keeps no clock and the retention decision is visible at the call
	// site rather than buried in a default.
	PurgeAfter time.Time
	// MarketingConsent is the guest's answer to the opt-in, not an assumption
	// drawn from their having booked.
	MarketingConsent bool
}
