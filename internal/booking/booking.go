// Package booking turns a price into a stay somebody is holding.
//
// It owns three things and stops there: the hold that reserves a pitch while a
// guest finishes checking out, the confirmation that freezes the agreed price
// onto a booking, and the state transitions reception drives at the desk.
// What a booking costs belongs to pricing, which has already finished by the
// time anything here runs; what a booking is worth belongs to billing, which
// does not exist yet.
//
// Two invariants carry the whole package. The exclusion constraint on
// unit_allocation is the only concurrency authority — nothing here counts
// inventory, takes a lock, or asks whether a pitch is free before writing, and
// the retry loop exists because 23P01 is the answer to that question. And a
// confirmed price is copied, never recomputed: after confirmation the pricing
// engine is never called for that booking again.
package booking

import (
	"crypto/rand"
	"errors"
	"fmt"
	"time"
)

// Sentinels the HTTP layer maps onto status codes. Each names something the
// caller can act on, which is why "no unit available" and "the price moved" are
// separate: one means try other dates, the other means look at the new total.
var (
	// ErrNoUnitAvailable means the category cannot host this stay. Either it was
	// full when asked, or it filled between the search and the request.
	ErrNoUnitAvailable = errors.New("booking: no unit available")

	// ErrHoldExpired means the hold the caller presented is gone and admission
	// could not be re-run in its place.
	ErrHoldExpired = errors.New("booking: hold expired")

	// ErrQuoteExpired means the quoted price is older than its window.
	ErrQuoteExpired = errors.New("booking: quote expired")

	// ErrQuoteMismatch means the request describes a different stay than the
	// quote priced. Refused rather than repriced: a guest who saw 3 200 kr must
	// be told the price moved, not charged 3 450.
	ErrQuoteMismatch = errors.New("booking: quote does not match the request")

	// ErrNotFound means no booking matches the reference or id.
	ErrNotFound = errors.New("booking: not found")

	// ErrWrongState means the transition asked for is not available from where
	// the booking is. Checking in a departed guest is not an error to paper
	// over.
	ErrWrongState = errors.New("booking: not in a state that allows this")
)

// HoldTTL is how long a pitch is held while a guest finishes checking out.
//
// Fifteen minutes with no payment in the flow is generous, and deliberately so:
// the guest is typing names and a phone number on a phone with one bar of
// signal, and a hold that expires mid-form turns into a 409 they did nothing to
// deserve. When payment lands this becomes per-method, because a Swish request
// expires in three minutes and a card redirect needs ten.
const HoldTTL = 15 * time.Minute

// AccessTokenTTL is how long the link in a confirmation email keeps working.
// Long enough to cover a booking made in January for August, plus the stay
// itself.
const AccessTokenTTL = 400 * 24 * time.Hour

// Occupancy states, mirroring the CHECK on unit_allocation. A booking has no
// state of its own: its allocation is the state, because the state that the
// exclusion constraint reads is the one deciding whether a pitch is
// double-booked.
const (
	StateHeld       = "held"
	StateConfirmed  = "confirmed"
	StateCheckedIn  = "checked_in"
	StateCheckedOut = "checked_out"
	StateCancelled  = "cancelled"
	StateExpired    = "expired"
	StateNoShow     = "no_show"
)

// Event kinds appended to reservation_event.
const (
	EventConfirmed  = "confirmed"
	EventCheckedIn  = "checked_in"
	EventCheckedOut = "checked_out"
	EventCancelled  = "cancelled"
	EventReassigned = "reassigned"
)

// referenceAlphabet excludes I, O, 0, 1, U and V. A reference is read aloud
// over the phone and copied off a screen by hand, and those are the characters
// people get wrong.
const referenceAlphabet = "ABCDEFGHJKLMNPQRSTWXYZ23456789"

// referenceLength is six characters, which is about 10^8 combinations. It is
// not a credential — booking_access_token is — so it only has to be unlikely to
// collide, and the unique constraint catches it when it does.
const referenceLength = 6

// NewReference generates a booking reference.
func NewReference() (string, error) {
	buf := make([]byte, referenceLength)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	out := make([]byte, referenceLength)
	for i, b := range buf {
		out[i] = referenceAlphabet[int(b)%len(referenceAlphabet)]
	}
	return string(out), nil
}

func wrap(what string, err error) error {
	return fmt.Errorf("%s: %w", what, err)
}
