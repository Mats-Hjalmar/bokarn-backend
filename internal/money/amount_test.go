package money

import (
	"errors"
	"testing"
)

func TestAddRefusesDifferentCurrencies(t *testing.T) {
	_, err := New(100, "SEK").Add(New(100, "NOK"))
	var mismatch ErrCurrencyMismatch
	if !errors.As(err, &mismatch) {
		t.Fatalf("err = %v, want a currency mismatch", err)
	}
}

func TestMulBPRoundsHalfAwayFromZero(t *testing.T) {
	cases := []struct {
		minor int64
		bp    int64
		want  int64
	}{
		{100, 10000, 100}, // 100%
		{100, 1200, 12},   // 12% of 1.00
		{101, 1200, 12},   // 12.12 -> 12
		{125, 1200, 15},   // 15.00 exactly
		{104, 1200, 12},   // 12.48 -> 12
		{105, 1200, 13},   // 12.60 -> 13
		{-105, 1200, -13}, // symmetric about zero
		{50, 5000, 25},    // exact half at the boundary rounds away
	}
	for _, c := range cases {
		got := New(c.minor, "SEK").MulBP(c.bp).Minor
		if got != c.want {
			t.Errorf("%d minor × %d bp = %d, want %d",
				c.minor, c.bp, got, c.want)
		}
	}
}

// A package price allocated across its components must not gain or lose an öre,
// however awkward the division. This is the property that keeps an invoice's
// lines summing to its total.
func TestSplitProRataIsExact(t *testing.T) {
	cases := []struct {
		total   int64
		weights []int64
	}{
		{100, []int64{1, 1, 1}},         // 33.33 three ways
		{1000, []int64{1, 2, 3}},        // uneven
		{1, []int64{1, 1, 1, 1}},        // less than one unit each
		{99999, []int64{7, 11, 13, 17}}, // primes
		{0, []int64{1, 1}},              // nothing to split
		{-100, []int64{1, 1, 1}},        // a refund splits too
	}

	for _, c := range cases {
		parts, err := New(c.total, "SEK").SplitProRata(c.weights)
		if err != nil {
			t.Fatalf("split %d over %v: %v", c.total, c.weights, err)
		}
		var sum int64
		for _, p := range parts {
			sum += p.Minor
			if p.Currency != "SEK" {
				t.Errorf("part lost its currency: %+v", p)
			}
		}
		if sum != c.total {
			t.Errorf("split %d over %v summed to %d",
				c.total, c.weights, sum)
		}
	}
}

func TestSplitProRataIsDeterministic(t *testing.T) {
	for range 20 {
		parts, err := New(100, "SEK").SplitProRata([]int64{1, 1, 1})
		if err != nil {
			t.Fatal(err)
		}
		got := [3]int64{parts[0].Minor, parts[1].Minor, parts[2].Minor}
		// The leftover öre goes to the first of the tied remainders, every time.
		if got != [3]int64{34, 33, 33} {
			t.Fatalf("split = %v, want [34 33 33] on every run", got)
		}
	}
}

func TestSplitProRataRejectsNonsense(t *testing.T) {
	if _, err := New(100, "SEK").SplitProRata(nil); err == nil {
		t.Error("splitting across no weights succeeded")
	}
	if _, err := New(100, "SEK").SplitProRata([]int64{0, 0}); err == nil {
		t.Error("splitting across zero total weight succeeded")
	}
	if _, err := New(100, "SEK").SplitProRata([]int64{1, -1}); err == nil {
		t.Error("a negative weight was accepted")
	}
}

func TestSumRefusesNothing(t *testing.T) {
	if _, err := Sum(); err == nil {
		t.Error("Sum() of nothing succeeded; it has no currency to report")
	}
}

func TestString(t *testing.T) {
	cases := map[int64]string{
		0:     "0.00 SEK",
		495:   "4.95 SEK",
		49500: "495.00 SEK",
		-2500: "-25.00 SEK",
	}
	for minor, want := range cases {
		if got := New(minor, "SEK").String(); got != want {
			t.Errorf("String(%d) = %q, want %q", minor, got, want)
		}
	}
}
