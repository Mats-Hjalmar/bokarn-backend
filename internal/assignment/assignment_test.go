package assignment

import "testing"

func TestCostPrefersTightFitOverBetterUnit(t *testing.T) {
	req := Request{Guests: 2, ElectricityAmp: 10}

	// A plain pitch that fits exactly, against a better one that would strand
	// three nights on either side.
	exact := Candidate{
		UnitID: "b", ElectricityAmp: 16, MaxOccupancy: 6,
	}
	fragmenting := Candidate{
		UnitID: "a", ElectricityAmp: 10, MaxOccupancy: 2,
		FreeBefore: 3, FreeAfter: 3,
	}

	if Cost(exact, req) >= Cost(fragmenting, req) {
		t.Errorf(
			"exact fit cost %d should beat fragmenting cost %d",
			Cost(exact, req), Cost(fragmenting, req))
	}
}

func TestCostPenalisesUnneededScarceAttributes(t *testing.T) {
	req := Request{Guests: 2}

	plain := Candidate{UnitID: "a", MaxOccupancy: 2}
	accessible := Candidate{UnitID: "b", MaxOccupancy: 2, Accessible: true}

	if Cost(plain, req) >= Cost(accessible, req) {
		t.Error(
			"an accessible unit must cost more when accessibility is not needed",
		)
	}

	// ...but not when it is what was asked for.
	needed := Request{Guests: 2, NeedsAccessible: true}
	if Cost(accessible, needed) != Cost(plain, needed) {
		t.Error("an accessible unit must not be penalised when required")
	}
}

func TestGapIsCappedAtLookahead(t *testing.T) {
	req := Request{Guests: 2}

	far := Candidate{UnitID: "a", MaxOccupancy: 2, FreeBefore: 90}
	capped := Candidate{UnitID: "a", MaxOccupancy: 2, FreeBefore: MaxLookahead}

	if Cost(far, req) != Cost(capped, req) {
		t.Errorf(
			"a 90-night gap (%d) should score as the cap (%d): beyond the "+
				"lookahead it is empty calendar, not fragmentation",
			Cost(far, req), Cost(capped, req))
	}
}

func TestBestIsDeterministicRegardlessOfInputOrder(t *testing.T) {
	req := Request{Guests: 2}
	a := Candidate{UnitID: "A05", MaxOccupancy: 2, SortOrder: 5}
	b := Candidate{UnitID: "A09", MaxOccupancy: 2, SortOrder: 9}

	forward, ok := Best([]Candidate{a, b}, req)
	if !ok {
		t.Fatal("Best found nothing")
	}
	reverse, _ := Best([]Candidate{b, a}, req)

	if forward.UnitID != reverse.UnitID {
		t.Errorf(
			"Best returned %s then %s: the same inputs must yield the same pitch",
			forward.UnitID,
			reverse.UnitID,
		)
	}
	if forward.UnitID != "A05" {
		t.Errorf("Best = %s, want the lower sort order A05", forward.UnitID)
	}
}

func TestBestBreaksExactTiesOnUnitID(t *testing.T) {
	req := Request{Guests: 2}
	a := Candidate{UnitID: "A02", MaxOccupancy: 2, SortOrder: 1}
	b := Candidate{UnitID: "A01", MaxOccupancy: 2, SortOrder: 1}

	got, _ := Best([]Candidate{a, b}, req)
	if got.UnitID != "A01" {
		t.Errorf("tie broke to %s, want A01", got.UnitID)
	}
}

func TestBestOnEmptyReportsNothing(t *testing.T) {
	if _, ok := Best(nil, Request{Guests: 2}); ok {
		t.Error("Best reported a candidate from an empty list")
	}
}
