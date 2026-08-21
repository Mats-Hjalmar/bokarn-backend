package pricing

import (
	"fmt"
	"sort"
	"time"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/money"
)

// Price prices a stay. It is pure: the same snapshot and request always produce
// the same bytes, which is what the breakdown hash depends on.
//
// The pipeline order is fixed and documented in findings/pricing-pipeline.md.
// Each step appends to the explain trace, and steps that change money do so by
// emitting a line — never by rewriting an earlier one, so the breakdown always
// sums to the total and a guest can see where every krona went.
func Price(s Snapshot, r Request) (Quote, error) {
	nights := r.Nights()
	if nights <= 0 {
		return Quote{}, ErrStayNotSellable{
			Reason: ReasonMinStay,
			Detail: "departure must be after arrival",
		}
	}

	days, err := stayDays(s, r, nights)
	if err != nil {
		return Quote{}, err
	}

	q := Quote{EngineVersion: EngineVersion, Nights: nights}
	cur := s.Plan.Currency

	// 2. Stay rules. A violation is a hard reject with a reason, never a silent
	// substitution of another plan or a clamped date: a guest told "no" can
	// change their dates, a guest shown a different stay cannot.
	if err := validateStay(days, r, nights); err != nil {
		return Quote{}, err
	}

	// 3. Per-night accommodation, extra people by age band, pets, vehicles.
	adults, children := classifyParty(s, r)

	// The category has to be able to host the party. Availability already
	// refuses these; pricing must agree, or a guest is quoted a stay the
	// campsite has nowhere to put and the mismatch only surfaces at check-in.
	if party := adults + len(children); s.CategoryMaxOccupancy > 0 &&
		party > s.CategoryMaxOccupancy {
		return Quote{}, ErrStayNotSellable{
			Reason: ReasonOverCapacity,
			Detail: fmt.Sprintf("%d guests exceeds the category maximum of %d",
				party, s.CategoryMaxOccupancy),
		}
	}
	if r.Pets > 0 && !s.CategoryPetsAllowed {
		return Quote{}, ErrStayNotSellable{
			Reason: ReasonPetsNotAllowed,
			Detail: "this category does not take pets",
		}
	}
	accommodation := money.Zero(cur)

	for i, d := range days {
		night := money.New(d.BaseMinor, cur)

		extraAdults := adults - d.IncludedAdults
		if extraAdults > 0 {
			night = addTo(night, money.New(
				d.AdultExtraMinor*int64(extraAdults), cur))
		}

		for _, c := range children {
			night = addTo(night, money.New(c.PricePerNightMinor, cur))
		}

		if r.Pets > 0 {
			night = addTo(night, money.New(d.PetMinor*int64(r.Pets), cur))
		}
		if r.Vehicles > 0 {
			night = addTo(night, money.New(
				d.VehicleMinor*int64(r.Vehicles), cur))
		}

		q.Lines = append(q.Lines, Line{
			Seq:            i + 1,
			Kind:           KindAccommodation,
			StayDate:       d.Day,
			Description:    s.Plan.Label(),
			Qty:            1,
			UnitGrossMinor: night.Minor,
			GrossMinor:     night.Minor,
			VATCode:        s.Plan.VATCode,
		})
		accommodation = addTo(accommodation, night)
	}

	q.Explain = append(q.Explain, Step{
		Rule: "accommodation",
		Detail: fmt.Sprintf(
			"%d nights, %d adults, %d children",
			nights,
			adults,
			len(children),
		),
		Effect: accommodation.Minor,
	})

	// 4. A derived plan is priced from its parent. Applied to the subtotal as
	// its own line so the nightly figures stay the parent's and stay readable.
	if s.Plan.DeriveOp != "" {
		delta := deriveDelta(s.Plan, accommodation)
		if delta.Minor != 0 {
			q.Lines = append(q.Lines, Line{
				Seq:         len(q.Lines) + 1,
				Kind:        KindDerived,
				Description: s.Plan.Label(),
				Qty:         1,
				GrossMinor:  delta.Minor,
				VATCode:     s.Plan.VATCode,
			})
			accommodation = addTo(accommodation, delta)
			q.Explain = append(q.Explain, Step{
				Rule:   "derived_plan",
				Detail: s.Plan.DeriveOp,
				Effect: delta.Minor,
			})
		}
	}

	// 5. Occupancy and lead-time adjusters, in priority order, clamped to the
	// plan's floor and ceiling. The clamp is why those columns exist.
	accommodation = applyAdjusters(&q, s, r, accommodation, nights)

	// 6. Length of stay, as a negative line rather than by rewriting nights.
	if d := losDiscount(s, nights, accommodation); d.Minor != 0 {
		q.Lines = append(q.Lines, Line{
			Seq:         len(q.Lines) + 1,
			Kind:        KindLOSDiscount,
			Description: fmt.Sprintf("%d+ nights", losThreshold(s, nights)),
			Qty:         1,
			GrossMinor:  d.Minor,
			VATCode:     s.Plan.VATCode,
		})
		q.Explain = append(q.Explain, Step{
			Rule: "los_discount", Detail: fmt.Sprintf("%d nights", nights),
			Effect: d.Minor,
		})
	}

	// 8. Campaign code.
	if s.Campaign != nil {
		subtotal := grossOf(q.Lines, cur)
		d := campaignDelta(*s.Campaign, subtotal)
		if d.Minor != 0 {
			q.Lines = append(q.Lines, Line{
				Seq:         len(q.Lines) + 1,
				Kind:        KindCampaign,
				Description: s.Campaign.Label(),
				Qty:         1,
				GrossMinor:  d.Minor,
				VATCode:     s.Plan.VATCode,
			})
			q.Explain = append(q.Explain, Step{
				Rule: "campaign", Detail: s.Campaign.Code, Effect: d.Minor,
			})
		}
	}

	// A discount can be larger than the stay — a 500 kr voucher on a 195 kr
	// night, or two campaigns stacking. The excess is written back as its own
	// line rather than by trimming an earlier one, so the breakdown still sums
	// and staff can see that a discount was capped.
	if total := grossOf(q.Lines, cur); total.Minor < 0 {
		q.Lines = append(q.Lines, Line{
			Seq:         len(q.Lines) + 1,
			Kind:        KindRounding,
			Description: "rabatt begränsad till ordervärdet",
			Qty:         1,
			GrossMinor:  -total.Minor,
			VATCode:     s.Plan.VATCode,
		})
		q.Explain = append(q.Explain, Step{
			Rule:   "discount_floor",
			Detail: "a discount exceeded the stay and was capped at zero",
			Effect: -total.Minor,
		})
	}

	// VAT, per line, once. Totals are the sum of the lines and are never
	// computed independently — that is what makes them impossible to disagree.
	if err := applyVAT(&q, s); err != nil {
		return Quote{}, err
	}

	q.Totals = totalsOf(q.Lines, cur)
	return q, nil
}

func stayDays(s Snapshot, r Request, nights int) ([]RateDay, error) {
	days := make([]RateDay, 0, nights)
	for i := range nights {
		day := addDays(r.Arrival, i)
		d, ok := s.Days[day]
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrNoRate, day)
		}
		days = append(days, d)
	}
	return days, nil
}

func validateStay(days []RateDay, r Request, nights int) error {
	first := days[0]

	for _, d := range days {
		if d.Closed {
			return ErrStayNotSellable{Reason: ReasonClosed, Detail: d.Day}
		}
	}
	if nights < first.MinStay {
		return ErrStayNotSellable{
			Reason: ReasonMinStay,
			Detail: fmt.Sprintf(
				"minimum %d nights from %s",
				first.MinStay,
				first.Day,
			),
		}
	}
	if first.MaxStay > 0 && nights > first.MaxStay {
		return ErrStayNotSellable{
			Reason: ReasonMaxStay,
			Detail: fmt.Sprintf(
				"maximum %d nights from %s",
				first.MaxStay,
				first.Day,
			),
		}
	}
	if first.ClosedToArrival {
		return ErrStayNotSellable{
			Reason: ReasonClosedToArrival,
			Detail: first.Day,
		}
	}
	// Saturday-to-Saturday cabins are expressed here, not in code.
	if first.ArrivalMask != 0 && first.ArrivalMask != 127 {
		if first.ArrivalMask&weekdayBit(first.Day) == 0 {
			return ErrStayNotSellable{
				Reason: ReasonArrivalDay,
				Detail: fmt.Sprintf("arrival not permitted on %s", first.Day),
			}
		}
	}
	if days[len(days)-1].ClosedToDeparture {
		return ErrStayNotSellable{
			Reason: ReasonClosedToDeparture,
			Detail: r.Departure,
		}
	}
	return nil
}

// classifyParty splits the party into adults and priced children. A child older
// than the highest band counts as an adult — a documented rule rather than a
// silent omission, and the only sane reading of "the bands stop at 15".
func classifyParty(s Snapshot, r Request) (adults int, children []AgeBand) {
	adults = r.Adults

	bands := append([]AgeBand(nil), s.AgeBands...)
	sort.Slice(
		bands,
		func(i, j int) bool { return bands[i].AgeFrom < bands[j].AgeFrom },
	)

	for _, c := range r.Children {
		age := ageAt(c.DateOfBirth, r.Arrival)
		matched := false
		for _, b := range bands {
			if age >= b.AgeFrom && age <= b.AgeTo {
				children = append(children, b)
				matched = true
				break
			}
		}
		if !matched {
			adults++
		}
	}
	return adults, children
}

func applyAdjusters(
	q *Quote, s Snapshot, r Request, subtotal money.Amount, nights int,
) money.Amount {
	adjusters := append([]Adjuster(nil), s.Adjusters...)
	sort.SliceStable(adjusters, func(i, j int) bool {
		if adjusters[i].Priority != adjusters[j].Priority {
			return adjusters[i].Priority < adjusters[j].Priority
		}
		return adjusters[i].Name < adjusters[j].Name
	})

	for _, a := range adjusters {
		if !adjusterApplies(a, r) {
			continue
		}

		var delta money.Amount
		if a.UsesFactor {
			// factor_bp is a multiplier: 11000 means +10%. The line records the
			// change, not the new total, so the breakdown still sums.
			delta = money.New(
				subtotal.MulBP(int64(a.FactorBP)).Minor-subtotal.Minor,
				subtotal.Currency,
			)
		} else {
			delta = money.New(a.DeltaMinor*int64(nights), subtotal.Currency)
		}

		after := addTo(subtotal, delta)
		clamped := clamp(after, s.Plan, nights)

		// A clamped adjuster is the commonest "why is this price not what I
		// configured" question, so the trace says so rather than reporting a
		// smaller effect with no explanation.
		detail := a.Name
		if clamped.Minor != after.Minor {
			detail = fmt.Sprintf("%s (begränsad av pristak/prisgolv, %s → %s)",
				a.Name,
				money.New(after.Minor, subtotal.Currency),
				money.New(clamped.Minor, subtotal.Currency))
			delta = money.New(clamped.Minor-subtotal.Minor, subtotal.Currency)
			after = clamped
		}
		if delta.Minor == 0 {
			continue
		}

		q.Lines = append(q.Lines, Line{
			Seq:         len(q.Lines) + 1,
			Kind:        KindAdjuster,
			Description: a.Name,
			Qty:         1,
			GrossMinor:  delta.Minor,
			VATCode:     s.Plan.VATCode,
		})
		q.Explain = append(q.Explain, Step{
			Rule: "adjuster", Detail: detail, Effect: delta.Minor,
		})
		subtotal = after
	}
	return subtotal
}

func adjusterApplies(a Adjuster, r Request) bool {
	if a.HasOccupancy &&
		(r.OccupancyBP < a.OccupancyFrom || r.OccupancyBP > a.OccupancyTo) {
		return false
	}
	if a.HasLeadDays &&
		(r.LeadDays < a.LeadDaysFrom || r.LeadDays > a.LeadDaysTo) {
		return false
	}
	return a.HasOccupancy || a.HasLeadDays
}

func clamp(a money.Amount, p RatePlan, nights int) money.Amount {
	if p.MinPriceMinor != nil {
		floor := *p.MinPriceMinor * int64(nights)
		if a.Minor < floor {
			return money.New(floor, a.Currency)
		}
	}
	if p.MaxPriceMinor != nil {
		ceiling := *p.MaxPriceMinor * int64(nights)
		if a.Minor > ceiling {
			return money.New(ceiling, a.Currency)
		}
	}
	return a
}

func losDiscount(s Snapshot, nights int, subtotal money.Amount) money.Amount {
	best := 0
	for _, d := range s.LOS {
		if nights >= d.MinNights && d.MinNights > best {
			best = d.MinNights
		}
	}
	if best == 0 {
		return money.Zero(subtotal.Currency)
	}
	for _, d := range s.LOS {
		if d.MinNights == best {
			return subtotal.MulBP(int64(d.PercentBP)).Neg()
		}
	}
	return money.Zero(subtotal.Currency)
}

func losThreshold(s Snapshot, nights int) int {
	best := 0
	for _, d := range s.LOS {
		if nights >= d.MinNights && d.MinNights > best {
			best = d.MinNights
		}
	}
	return best
}

func campaignDelta(c Campaign, subtotal money.Amount) money.Amount {
	if c.Kind == "percent" {
		return subtotal.MulBP(c.Value).Neg()
	}
	return money.New(-c.Value, subtotal.Currency)
}

func deriveDelta(p RatePlan, subtotal money.Amount) money.Amount {
	if p.DeriveOp == "percent" {
		return subtotal.MulBP(int64(p.DeriveValueBP))
	}
	return money.New(int64(p.DeriveValueBP), subtotal.Currency)
}

// applyVAT splits every line into net and VAT at its own rate. Prices are
// stored and quoted gross, which is correct for Nordic B2C, so the split runs
// backwards out of the gross and is rounded once per line.
func applyVAT(q *Quote, s Snapshot) error {
	for i := range q.Lines {
		code := q.Lines[i].VATCode
		vat, ok := s.VATCodes[code]
		if !ok {
			return fmt.Errorf("%w: %s", ErrNoVATCode, code)
		}
		gross := money.New(q.Lines[i].GrossMinor, s.Plan.Currency)
		net := netFromGross(gross, vat.RateBP)

		q.Lines[i].VATRateBP = vat.RateBP
		q.Lines[i].VATTreatment = vat.Treatment
		q.Lines[i].NetMinor = net.Minor
		q.Lines[i].VATMinor = gross.Minor - net.Minor
	}
	return nil
}

// netFromGross backs VAT out of a gross amount: net = gross × 10000 /
// (10000 + rate). Rounded half away from zero, which is the rule Swedish
// invoices are checked against.
func netFromGross(gross money.Amount, rateBP int) money.Amount {
	if rateBP == 0 {
		return gross
	}
	return money.New(
		divRound(gross.Minor*10000, int64(10000+rateBP)), gross.Currency)
}

func divRound(n, d int64) int64 {
	if (n < 0) != (d < 0) {
		return (n - d/2) / d
	}
	return (n + d/2) / d
}

func totalsOf(lines []Line, currency string) Totals {
	t := Totals{Currency: currency, ByRate: map[string]int64{}}
	for _, l := range lines {
		t.GrossMinor += l.GrossMinor
		t.NetMinor += l.NetMinor
		t.VATMinor += l.VATMinor
		t.ByRate[fmt.Sprintf("%d", l.VATRateBP)] += l.VATMinor
	}
	return t
}

func grossOf(lines []Line, currency string) money.Amount {
	var total int64
	for _, l := range lines {
		total += l.GrossMinor
	}
	return money.New(total, currency)
}

// addTo adds inside the engine, where both operands are known to share a
// currency by construction. A mismatch here would be a programming error, not a
// data error, so it panics rather than threading an impossible error upward.
func addTo(a, b money.Amount) money.Amount {
	sum, err := a.Add(b)
	if err != nil {
		panic(err)
	}
	return sum
}

func daysBetween(from, to string) int {
	a, err1 := time.Parse(time.DateOnly, from)
	b, err2 := time.Parse(time.DateOnly, to)
	if err1 != nil || err2 != nil {
		return 0
	}
	return int(b.Sub(a).Hours() / 24)
}

func addDays(date string, n int) string {
	d, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return date
	}
	return d.AddDate(0, 0, n).Format(time.DateOnly)
}

func weekdayBit(date string) int {
	d, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return 0
	}
	// Monday = bit 0, matching the mask staff edit.
	return 1 << ((int(d.Weekday()) + 6) % 7)
}

func ageAt(dob, on string) int {
	b, err1 := time.Parse(time.DateOnly, dob)
	d, err2 := time.Parse(time.DateOnly, on)
	if err1 != nil || err2 != nil {
		return 0
	}
	age := d.Year() - b.Year()
	if d.YearDay() < b.YearDay() {
		age--
	}
	return age
}
