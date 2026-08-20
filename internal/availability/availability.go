// Package availability answers what an operator has free for a stay. It returns
// counts per category, never named units: the guest buys a category and the
// physical unit is chosen at hold time by the assignment package.
//
// Search has one signature on purpose. If category-grain admission is ever
// needed — selling without assigning, which is what deliberate overbooking
// requires — a counter-backed implementation drops in behind it without any
// caller changing.
package availability

// Query is a stay someone is looking for. Dates are ISO calendar dates in the
// site's timezone; a night is a date, never an instant.
type Query struct {
	Arrival   string
	Departure string
	Adults    int
	Children  int
	Pets      int
	// ElectricityAmp filters to units supplying at least this many amps. Zero
	// means the guest did not ask, not that they want none.
	ElectricityAmp int
	Accessible     bool
}

// Guests is the party size a unit has to hold.
func (q Query) Guests() int { return q.Adults + q.Children }

// CategoryOffer is one category with how much of it is left.
type CategoryOffer struct {
	Code         string `json:"code"`
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	MaxOccupancy int    `json:"max_occupancy"`
	Free         int    `json:"free"          desc:"Free for the whole stay"`
}
