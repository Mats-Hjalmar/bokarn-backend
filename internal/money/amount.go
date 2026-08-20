// Package money is exact decimal arithmetic in minor units. It exists so that
// no price calculation anywhere in bokarn ever touches a float, and so that an
// amount can never be added to one in another currency.
//
// Everything here is pure: no clock, no database, no configuration. The pricing
// engine depends on it and on nothing else.
package money

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// Amount is a quantity of money. Minor is the smallest unit of the currency —
// öre for SEK — and Currency is its ISO 4217 code. The two travel together
// because an amount without its currency is a number that looks like money and
// silently is not.
type Amount struct {
	Minor    int64
	Currency string
}

// New constructs an Amount.
func New(minor int64, currency string) Amount {
	return Amount{Minor: minor, Currency: strings.ToUpper(currency)}
}

// Zero is a zero amount in the given currency. It is distinct from the empty
// Amount{}, which has no currency and cannot be added to anything.
func Zero(currency string) Amount { return New(0, currency) }

// IsZero reports whether the amount is nil-valued.
func (a Amount) IsZero() bool { return a.Minor == 0 }

// Neg returns the amount with its sign flipped. Discounts are negative lines
// rather than subtractions applied elsewhere, so the breakdown always sums.
func (a Amount) Neg() Amount {
	return Amount{Minor: -a.Minor, Currency: a.Currency}
}

// ErrOverflow is returned when a result does not fit in an int64.
//
// Defence in depth: the API bounds party sizes and stay lengths so this should
// be unreachable, but an overflow that wraps silently turns a price negative
// and persists it, which is the worst possible failure for a money type.
var ErrOverflow = errors.New("money: amount overflows int64")

// ErrCurrencyMismatch is returned when amounts in different currencies meet.
type ErrCurrencyMismatch struct{ A, B string }

func (e ErrCurrencyMismatch) Error() string {
	return fmt.Sprintf("money: cannot combine %s and %s", e.A, e.B)
}

// Add returns a+b, or an error when the currencies differ.
func (a Amount) Add(b Amount) (Amount, error) {
	if err := a.compatible(b); err != nil {
		return Amount{}, err
	}
	sum := a.Minor + b.Minor
	if (a.Minor > 0 && b.Minor > 0 && sum < 0) ||
		(a.Minor < 0 && b.Minor < 0 && sum > 0) {
		return Amount{}, ErrOverflow
	}
	return Amount{Minor: sum, Currency: a.currencyOf(b)}, nil
}

// Sub returns a-b, or an error when the currencies differ.
func (a Amount) Sub(b Amount) (Amount, error) {
	if err := a.compatible(b); err != nil {
		return Amount{}, err
	}
	return a.Add(Amount{Minor: -b.Minor, Currency: b.Currency})
}

// Sum adds any number of amounts. An empty sum has no currency to report and is
// an error rather than a zero, because a total that silently loses its currency
// is the bug this package exists to prevent.
func Sum(amounts ...Amount) (Amount, error) {
	if len(amounts) == 0 {
		return Amount{}, fmt.Errorf("money: cannot sum nothing")
	}
	total := amounts[0]
	for _, a := range amounts[1:] {
		next, err := total.Add(a)
		if err != nil {
			return Amount{}, err
		}
		total = next
	}
	return total, nil
}

// MulBP multiplies by a rate in basis points, rounding half away from zero.
// Basis points rather than a float: 12.5% is 1250, exactly, on every machine.
func (a Amount) MulBP(bp int64) Amount {
	product, err := mul(a.Minor, bp)
	if err != nil {
		// Reachable only past the API's bounds on party size and stay length.
		// Saturating is the least-wrong answer for a helper with no error
		// return: a clamped extreme is visibly wrong, a wrapped one is not.
		return Amount{Minor: saturate(a.Minor, bp), Currency: a.Currency}
	}
	return Amount{Minor: divRound(product, 10000), Currency: a.Currency}
}

// MulBPChecked is MulBP with the overflow reported rather than saturated.
func (a Amount) MulBPChecked(bp int64) (Amount, error) {
	product, err := mul(a.Minor, bp)
	if err != nil {
		return Amount{}, err
	}
	return Amount{Minor: divRound(product, 10000), Currency: a.Currency}, nil
}

func mul(a, b int64) (int64, error) {
	if a == 0 || b == 0 {
		return 0, nil
	}
	product := a * b
	if product/b != a {
		return 0, ErrOverflow
	}
	return product, nil
}

func saturate(a, bp int64) int64 {
	if (a > 0) == (bp > 0) {
		return math.MaxInt64
	}
	return math.MinInt64
}

// SplitProRata divides the amount across weights so the parts sum exactly to
// the whole. The remainder goes to the largest fractional parts first, ties
// broken by position, which makes the result deterministic — a package price
// allocated over its components must not depend on map ordering.
func (a Amount) SplitProRata(weights []int64) ([]Amount, error) {
	if len(weights) == 0 {
		return nil, fmt.Errorf("money: cannot split across no weights")
	}

	var total int64
	for _, w := range weights {
		if w < 0 {
			return nil, fmt.Errorf("money: negative weight %d", w)
		}
		total += w
	}
	if total == 0 {
		return nil, fmt.Errorf("money: cannot split across zero total weight")
	}

	// Split the magnitude and re-apply the sign, rather than dividing a
	// negative directly: Go truncates toward zero, so the leftover units of a
	// negative total come out negative and a "hand out one at a time" loop
	// counting upward never runs. A refund would then be an öre short of what
	// was charged, which is the kind of discrepancy nobody notices until an
	// invoice fails to reconcile.
	magnitude := a.Minor
	negative := magnitude < 0
	if negative {
		magnitude = -magnitude
	}

	parts := make([]Amount, len(weights))
	remainders := make([]int64, len(weights))
	var allocated int64

	for i, w := range weights {
		exact := magnitude * w
		share := exact / total
		parts[i] = Amount{Minor: share, Currency: a.Currency}
		remainders[i] = exact - share*total
		allocated += share
	}

	// Hand out the leftover minor units one at a time, largest remainder first,
	// ties by position — so the same inputs always produce the same split.
	for left := magnitude - allocated; left > 0; left-- {
		best := 0
		for i := range remainders {
			if remainders[i] > remainders[best] {
				best = i
			}
		}
		parts[best].Minor++
		remainders[best] = -1
	}

	if negative {
		for i := range parts {
			parts[i].Minor = -parts[i].Minor
		}
	}

	return parts, nil
}

// String renders the amount for logs and errors, not for display: formatting
// for a guest is the frontend's job and depends on their locale.
func (a Amount) String() string {
	sign := ""
	minor := a.Minor
	if minor < 0 {
		sign, minor = "-", -minor
	}
	return fmt.Sprintf("%s%d.%02d %s", sign, minor/100, minor%100, a.Currency)
}

func (a Amount) compatible(b Amount) error {
	if a.Currency == "" || b.Currency == "" || a.Currency == b.Currency {
		return nil
	}
	return ErrCurrencyMismatch{A: a.Currency, B: b.Currency}
}

// currencyOf resolves the currency of a result, tolerating a currency-less zero
// on either side so that Sum can start from one.
func (a Amount) currencyOf(b Amount) string {
	if a.Currency != "" {
		return a.Currency
	}
	return b.Currency
}

// divRound divides rounding half away from zero, which is the rule Swedish
// invoices are checked against and the one Klarna validates totals with.
func divRound(n, d int64) int64 {
	if (n < 0) != (d < 0) {
		return (n - d/2) / d
	}
	return (n + d/2) / d
}
