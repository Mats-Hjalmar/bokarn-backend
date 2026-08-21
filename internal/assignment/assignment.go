// Package assignment picks which physical unit a category booking lands on.
//
// The guest buys a category; this decides the pitch. The decision is
// provisional until check-in, so staff may reassign freely, which is what keeps
// a first-fit choice from being a commitment.
//
// Cost is a pure function with no database handle, no clock and no randomness.
// Its table test is the specification: an assignment rule nobody can read back
// from a test is one nobody can argue with when a guest complains.
package assignment

import "sort"

// Candidate is one unit that already passed the hard filters — right category,
// in season, no overlapping allocation. What remains is preference.
type Candidate struct {
	UnitID    string
	SortOrder int

	// Nights left free immediately before and after the stay, capped by the
	// caller's lookahead. Zero on both sides is an exact fit.
	FreeBefore int
	FreeAfter  int

	ElectricityAmp int
	MaxOccupancy   int
	Accessible     bool
	Sanitary       bool
	HasView        bool
}

// Request is what the stay actually needs.
type Request struct {
	Guests          int
	ElectricityAmp  int
	NeedsAccessible bool
	NeedsSanitary   bool
}

// Weights. Fragmentation costs an order of magnitude more than surplus, because
// a stranded single night is revenue that cannot be sold at all, whereas
// spending a better pitch than necessary only forgoes an upsell.
const (
	gapWeight     = 100
	surplusWeight = 10

	// A gap wider than a fortnight is not fragmentation, it is just an empty
	// stretch of calendar, and counting it would make distant units look
	// arbitrarily bad.
	MaxLookahead = 14
)

// Cost scores a candidate; lower is better. It never returns a tie for two
// distinct units, because SortOrder breaks it and the caller orders by UnitID
// after that.
func Cost(c Candidate, r Request) int {
	gap := clamp(c.FreeBefore) + clamp(c.FreeAfter)

	surplus := 0
	if r.ElectricityAmp > 0 && c.ElectricityAmp > r.ElectricityAmp {
		surplus += c.ElectricityAmp - r.ElectricityAmp
	}
	if c.MaxOccupancy > r.Guests {
		surplus += c.MaxOccupancy - r.Guests
	}
	// Burning a scarce attribute on a stay that did not ask for it is the
	// commonest way a system quietly loses the ability to house someone who
	// needs it.
	if c.Accessible && !r.NeedsAccessible {
		surplus += 5
	}
	if c.Sanitary && !r.NeedsSanitary {
		surplus += 3
	}
	if c.HasView {
		surplus += 2
	}

	return gap*gapWeight + surplus*surplusWeight + c.SortOrder
}

// Best returns the lowest-cost candidate. The order of the input does not
// affect the result: ties fall to the lower UnitID, so the same inputs always
// produce the same pitch.
func Best(candidates []Candidate, r Request) (Candidate, bool) {
	var best Candidate
	bestCost := 0
	found := false

	for _, c := range candidates {
		cost := Cost(c, r)
		switch {
		case !found, cost < bestCost,
			cost == bestCost && c.UnitID < best.UnitID:
			best, bestCost, found = c, cost, true
		}
	}
	return best, found
}

func clamp(nights int) int {
	if nights < 0 {
		return 0
	}
	if nights > MaxLookahead {
		return MaxLookahead
	}
	return nights
}

// Order sorts candidates best first, so the caller can walk them as a
// first-fit list. The order is total: Cost never ties two distinct units
// without SortOrder separating them, and UnitID breaks anything left.
func Order(candidates []Candidate, r Request) {
	sort.SliceStable(candidates, func(i, j int) bool {
		ci, cj := Cost(candidates[i], r), Cost(candidates[j], r)
		if ci != cj {
			return ci < cj
		}
		return candidates[i].UnitID < candidates[j].UnitID
	})
}
