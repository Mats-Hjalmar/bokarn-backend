package httpx

import (
	"errors"
	"fmt"
	"time"
)

// Bounds on a stay request. These are not arbitrary: a party larger than
// maxParty cannot fit any category bokarn models, and unbounded counts multiply
// into an int64 overflow that turns a price negative — found by testing, not by
// reading the code.
const (
	maxParty         = 60
	maxPets          = 20
	maxVehicles      = 20
	maxStayNights    = 365
	oldestGuestYears = 120
)

// validateStayWindow checks the dates a stay could plausibly cover.
//
// Yesterday is allowed as an arrival, and only yesterday: a guest booking at
// midnight in a different timezone, or a receptionist entering a walk-in who
// arrived last night, are both real. Anything older is a typo, and pricing a
// stay in the past produces a bill nobody can act on.
func validateStayWindow(arrival, departure string) error {
	from, to, err := parseStay(arrival, departure)
	if err != nil {
		return err
	}

	today := time.Now().Truncate(24 * time.Hour)
	if from.Before(today.AddDate(0, 0, -1)) {
		return errors.New("arrival is in the past")
	}
	if nights := int(to.Sub(from).Hours() / 24); nights > maxStayNights {
		return fmt.Errorf("a stay may be at most %d nights", maxStayNights)
	}
	return nil
}

// validateParty bounds the counts.
func validateParty(adults, children, pets, vehicles int) error {
	switch {
	case adults < 1:
		return errors.New("at least one adult is required")
	case children < 0 || pets < 0 || vehicles < 0:
		return errors.New("counts cannot be negative")
	case adults > maxParty, children > maxParty,
		adults+children > maxParty:
		return fmt.Errorf("a party may be at most %d guests", maxParty)
	case pets > maxPets, vehicles > maxVehicles:
		return errors.New("too many pets or vehicles")
	}
	return nil
}

// validateChildBirthDate rejects a date that would silently reclassify the
// child. A birth date after arrival, or implausibly long before it, is a typo,
// and accepting it prices a child as an adult.
func validateChildBirthDate(dateOfBirth string, arrival time.Time) error {
	dob, err := time.Parse(time.DateOnly, dateOfBirth)
	if err != nil {
		return errors.New("every child needs a date of birth, YYYY-MM-DD")
	}
	oldest := arrival.AddDate(-oldestGuestYears, 0, 0)
	if dob.After(arrival) || dob.Before(oldest) {
		return errors.New(
			"a child's date of birth must be before arrival and plausible")
	}
	return nil
}
