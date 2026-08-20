package pricing

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fixture is the on-disk shape of a golden case. A revenue manager files a
// pricing bug by adding one of these; the test below walks every file in
// testdata/, so nothing has to be registered anywhere.
type fixture struct {
	Name     string   `json:"name"`
	Snapshot Snapshot `json:"snapshot"`
	Request  Request  `json:"request"`
	Expected *Quote   `json:"expected,omitempty"`
	Reject   string   `json:"reject,omitempty"`
}

func TestGoldenFixtures(t *testing.T) {
	files, err := filepath.Glob("testdata/*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no fixtures found; the specification is empty")
	}

	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var f fixture
			if err := json.Unmarshal(raw, &f); err != nil {
				t.Fatalf("parse fixture: %v", err)
			}

			got, err := Price(f.Snapshot, f.Request)

			if f.Reject != "" {
				var notSellable ErrStayNotSellable
				if !errors.As(err, &notSellable) {
					t.Fatalf("err = %v, want a rejection with reason %q",
						err, f.Reject)
				}
				if notSellable.Reason != f.Reject {
					t.Errorf(
						"reason = %q, want %q",
						notSellable.Reason,
						f.Reject,
					)
				}
				return
			}

			if err != nil {
				t.Fatalf("Price: %v", err)
			}
			// A fixture may assert only the invariants, which the tests below
			// apply to every file regardless.
			if f.Expected != nil {
				assertQuote(t, got, *f.Expected)
			}
		})
	}
}

func assertQuote(t *testing.T, got, want Quote) {
	t.Helper()

	if got.Totals.GrossMinor != want.Totals.GrossMinor {
		t.Errorf("gross = %d, want %d",
			got.Totals.GrossMinor, want.Totals.GrossMinor)
	}
	if got.Totals.NetMinor != want.Totals.NetMinor {
		t.Errorf("net = %d, want %d", got.Totals.NetMinor, want.Totals.NetMinor)
	}
	if got.Totals.VATMinor != want.Totals.VATMinor {
		t.Errorf("vat = %d, want %d", got.Totals.VATMinor, want.Totals.VATMinor)
	}
	if len(want.Lines) > 0 && len(got.Lines) != len(want.Lines) {
		t.Errorf("%d lines, want %d", len(got.Lines), len(want.Lines))
		for _, l := range got.Lines {
			t.Logf("  %s %s %d", l.Kind, l.StayDate, l.GrossMinor)
		}
	}
}

// These hold for every quote the engine will ever produce, so they are asserted
// against the fixtures rather than restated in each one.
func TestInvariantsHoldForEveryFixture(t *testing.T) {
	files, _ := filepath.Glob("testdata/*.json")

	for _, file := range files {
		raw, _ := os.ReadFile(file)
		var f fixture
		if json.Unmarshal(raw, &f) != nil || f.Reject != "" {
			continue
		}

		t.Run(filepath.Base(file), func(t *testing.T) {
			q, err := Price(f.Snapshot, f.Request)
			if err != nil {
				t.Fatalf("Price: %v", err)
			}

			var gross, net, vat int64
			for _, l := range q.Lines {
				if l.NetMinor+l.VATMinor != l.GrossMinor {
					t.Errorf("line %d: net %d + vat %d != gross %d",
						l.Seq, l.NetMinor, l.VATMinor, l.GrossMinor)
				}
				gross += l.GrossMinor
				net += l.NetMinor
				vat += l.VATMinor
			}
			if gross != q.Totals.GrossMinor {
				t.Errorf("lines sum to %d, totals say %d",
					gross, q.Totals.GrossMinor)
			}
			if net != q.Totals.NetMinor || vat != q.Totals.VATMinor {
				t.Errorf("net/vat totals disagree with the lines")
			}
		})
	}
}

// The breakdown hash is taken over the engine's output, so an unstable output
// would silently break the freeze on confirm.
func TestPriceIsDeterministic(t *testing.T) {
	files, _ := filepath.Glob("testdata/*.json")

	for _, file := range files {
		raw, _ := os.ReadFile(file)
		var f fixture
		if json.Unmarshal(raw, &f) != nil || f.Reject != "" {
			continue
		}

		first, err := Price(f.Snapshot, f.Request)
		if err != nil {
			continue
		}
		want, _ := json.Marshal(first)

		for range 25 {
			again, err := Price(f.Snapshot, f.Request)
			if err != nil {
				t.Fatalf("%s: became an error on repeat: %v", file, err)
			}
			got, _ := json.Marshal(again)
			if string(got) != string(want) {
				t.Fatalf("%s: output changed between identical calls", file)
			}
		}
	}
}

func TestMissingRateIsAnErrorNotAFreeNight(t *testing.T) {
	s := baseSnapshot()
	delete(s.Days, "2027-07-03")

	_, err := Price(s, Request{
		Arrival: "2027-07-01", Departure: "2027-07-05", Adults: 2,
	})
	if !errors.Is(err, ErrNoRate) {
		t.Fatalf(
			"err = %v, want ErrNoRate — an uncompiled night must not price as zero",
			err,
		)
	}
}

func TestUnresolvableVATCodeIsAnError(t *testing.T) {
	s := baseSnapshot()
	s.VATCodes = map[string]VATCode{}

	_, err := Price(s, Request{
		Arrival: "2027-07-01", Departure: "2027-07-03", Adults: 2,
	})
	if !errors.Is(err, ErrNoVATCode) {
		t.Fatalf("err = %v, want ErrNoVATCode", err)
	}
}

// A fifteen-year-old prices on the teen band; a seventeen-year-old is an adult,
// because the bands stop at 15 and inventing a price for them would be worse
// than charging the adult rate.
func TestChildrenPriceOnTheirAgeBand(t *testing.T) {
	s := baseSnapshot()
	s.AgeBands = []AgeBand{
		{Code: "0-3", AgeFrom: 0, AgeTo: 3, PricePerNightMinor: 0},
		{Code: "4-12", AgeFrom: 4, AgeTo: 12, PricePerNightMinor: 5000},
		{Code: "13-15", AgeFrom: 13, AgeTo: 15, PricePerNightMinor: 8000},
	}

	adults, children := classifyParty(s, Request{
		Arrival: "2027-07-01",
		Adults:  2,
		Children: []Guest{
			{DateOfBirth: "2025-01-01"}, // 2 — free band
			{DateOfBirth: "2018-01-01"}, // 9
			{DateOfBirth: "2012-01-01"}, // 15
			{DateOfBirth: "2009-01-01"}, // 18 — an adult
		},
	})

	if adults != 3 {
		t.Errorf(
			"adults = %d, want 3 (the eighteen-year-old counts as one)",
			adults,
		)
	}
	if len(children) != 3 {
		t.Fatalf("%d priced children, want 3", len(children))
	}
	want := []int64{0, 5000, 8000}
	for i, c := range children {
		if c.PricePerNightMinor != want[i] {
			t.Errorf(
				"child %d priced %d, want %d",
				i,
				c.PricePerNightMinor,
				want[i],
			)
		}
	}
}

func baseSnapshot() Snapshot {
	days := map[string]RateDay{}
	for _, d := range []string{
		"2027-07-01", "2027-07-02", "2027-07-03", "2027-07-04",
	} {
		days[d] = RateDay{
			Day: d, BaseMinor: 49500, IncludedAdults: 2,
			MinStay: 1, ArrivalMask: 127,
		}
	}
	return Snapshot{
		Plan: RatePlan{
			Code: "standard", Currency: "SEK", VATCode: "logi",
		},
		Days: days,
		VATCodes: map[string]VATCode{
			"logi": {Code: "logi", RateBP: 1200, Treatment: "standard"},
		},
	}
}
