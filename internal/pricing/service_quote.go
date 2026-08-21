package pricing

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/db"
	"github.com/jackc/pgx/v5"
)

// QuoteTTL is how long a quoted price is honoured. Long enough to finish a
// checkout, short enough that a rate change reaches the market the same day.
const QuoteTTL = 30 * time.Minute

// StoredQuote is a persisted quote, as returned to the caller and as reloaded
// on confirm.
type StoredQuote struct {
	ID            string `json:"id"`
	CategoryCode  string `json:"category_code"`
	CategoryName  string `json:"category_name"`
	Arrival       string `json:"arrival"`
	Departure     string `json:"departure"`
	ExpiresAt     string `json:"expires_at"`
	InputHash     string `json:"input_hash"`
	BreakdownHash string `json:"breakdown_hash"`
	Quote

	// Resolved identifiers, for the confirm path rather than for the guest.
	// Withheld from the response because a rate plan id on a public booking
	// page is an invitation to ask for a different one.
	SiteID     string    `json:"-"`
	CategoryID string    `json:"-"`
	RatePlanID string    `json:"-"`
	Expires    time.Time `json:"-"`
}

// CreateQuote prices a stay and stores the result.
//
// The breakdown is stored whole rather than as inputs to be re-evaluated:
// reconstructing a historical price from rate rules that have since been edited
// is precisely what the quote table exists to prevent. `now` is a parameter so
// the pricing path has no hidden clock; the caller supplies it once.
func (s *Store) CreateQuote(
	ctx context.Context,
	siteID, categoryCode string,
	req Request,
	now time.Time,
) (StoredQuote, error) {
	snap, planID, err := s.LoadSnapshot(
		ctx, siteID, categoryCode, req.Arrival, req.Departure, req.CampaignCode)
	if err != nil {
		return StoredQuote{}, err
	}

	// Hashed before the server fills in what the guest did not choose. LeadDays
	// and OccupancyBP are derived from the clock and from how full the site is,
	// so a hash covering them could never be recomputed on confirm — which is
	// the one thing input_hash exists to do.
	inputHash := InputHash(categoryCode, req)

	req.LeadDays = daysBetween(now.Format(time.DateOnly), req.Arrival)
	if req.LeadDays < 0 {
		req.LeadDays = 0
	}

	occupancy, resolvedSite, categoryID, categoryName, err :=
		s.categoryOccupancy(ctx, siteID, categoryCode, req.Arrival,
			req.Departure)
	if err != nil {
		return StoredQuote{}, err
	}
	req.OccupancyBP = occupancy

	q, err := Price(snap, req)
	if err != nil {
		return StoredQuote{}, err
	}

	breakdownHash := hashOf(q)

	payload, err := json.Marshal(q)
	if err != nil {
		return StoredQuote{}, fmt.Errorf("encode quote: %w", err)
	}

	expires := now.Add(QuoteTTL)
	out := StoredQuote{
		CategoryCode:  categoryCode,
		CategoryName:  categoryName,
		Arrival:       req.Arrival,
		Departure:     req.Departure,
		ExpiresAt:     expires.Format(time.RFC3339),
		InputHash:     fmt.Sprintf("%x", inputHash),
		BreakdownHash: fmt.Sprintf("%x", breakdownHash),
		Quote:         q,
	}

	err = s.db.Tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			insert into quote (
			    site_id, category_id, rate_plan_id, arrival, departure,
			    currency, engine_version, input_hash, breakdown_hash, payload,
			    total_gross_minor, total_net_minor, total_vat_minor, expires_at)
			values ($1, $2, $3, $4::date, $5::date, $6, $7, $8, $9, $10,
			        $11, $12, $13, $14)
			returning id::text`,
			resolvedSite, categoryID, planID, req.Arrival, req.Departure,
			snap.Plan.Currency, EngineVersion, inputHash[:], breakdownHash[:],
			payload, q.Totals.GrossMinor, q.Totals.NetMinor, q.Totals.VATMinor,
			expires,
		).Scan(&out.ID)
	})
	if err != nil {
		return StoredQuote{}, fmt.Errorf("insert quote: %w", err)
	}
	return out, nil
}

// categoryOccupancy is how full the category already is across the stay, in
// basis points, which is what the occupancy adjusters read. It is computed here
// rather than inside the engine so the engine keeps no database handle.
func (s *Store) categoryOccupancy(
	ctx context.Context,
	siteID, categoryCode, arrival, departure string,
) (
	occupancyBP int,
	resolvedSite, categoryID, categoryName string,
	err error,
) {
	err = s.db.ReadTx(ctx, func(tx pgx.Tx) error {
		var total, taken int
		err := tx.QueryRow(ctx, `
			select c.site_id::text, c.id::text, c.name,
			       count(u.id),
			       count(u.id) filter (where exists (
			           select 1 from unit_allocation a
			            where a.unit_id = u.id
			              and a.state in ('held', 'confirmed',
			                              'checked_in', 'checked_out')
			              and a.stay && daterange($2::date, $3::date)
			       ))
			  from unit_category c
			  join unit u on u.category_id = c.id and u.status = 'active'
			 where c.code = $1 and ($4 = '' or c.site_id::text = $4)
			 group by c.site_id, c.id, c.name`,
			categoryCode, arrival, departure, siteID,
		).Scan(&resolvedSite, &categoryID, &categoryName, &total, &taken)
		if err != nil {
			return fmt.Errorf("read category occupancy: %w", err)
		}
		if total > 0 {
			occupancyBP = taken * 10000 / total
		}
		return nil
	})
	return occupancyBP, resolvedSite, categoryID, categoryName, err
}

// LoadQuote reloads a stored quote.
func (s *Store) LoadQuote(
	ctx context.Context,
	id string,
) (StoredQuote, error) {
	var out StoredQuote
	err := s.db.ReadTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = s.LoadQuoteOn(ctx, tx, id)
		return err
	})
	return out, err
}

// LoadQuoteOn reloads a stored quote on the caller's transaction, which is what
// the confirm path uses: the price it copies and the availability it re-checks
// have to be one atomic read of the world.
func (s *Store) LoadQuoteOn(
	ctx context.Context,
	q db.TX,
	id string,
) (StoredQuote, error) {
	var out StoredQuote
	var payload, inputHash, breakdownHash []byte

	err := q.QueryRow(ctx, `
		select q.id::text, c.code, c.name, q.arrival::text, q.departure::text,
		       q.expires_at, q.input_hash, q.breakdown_hash, q.payload,
		       q.site_id::text, q.category_id::text, q.rate_plan_id::text
		  from quote q
		  join unit_category c on c.id = q.category_id
		 where q.id = $1`, id,
	).Scan(&out.ID, &out.CategoryCode, &out.CategoryName,
		&out.Arrival, &out.Departure,
		&out.Expires, &inputHash, &breakdownHash, &payload,
		&out.SiteID, &out.CategoryID, &out.RatePlanID)
	switch {
	case err == nil:
	case errors.Is(err, pgx.ErrNoRows), db.IsBadInput(err):
		return StoredQuote{}, ErrQuoteNotFound
	default:
		return StoredQuote{}, fmt.Errorf("load quote: %w", err)
	}

	if err := json.Unmarshal(payload, &out.Quote); err != nil {
		return StoredQuote{}, fmt.Errorf("decode quote payload: %w", err)
	}
	out.ExpiresAt = out.Expires.Format(time.RFC3339)
	out.InputHash = fmt.Sprintf("%x", inputHash)
	out.BreakdownHash = fmt.Sprintf("%x", breakdownHash)
	return out, nil
}

// CancellationTerms is the ladder a rate plan sells under, as a JSON document
// ready to be frozen onto a booking.
//
// It returns nil when the plan names no policy. That is not "free
// cancellation": it means the operator has not said, and the cancel path must
// refuse to compute a fee rather than invent one. Encoding it here as an
// explicit nil keeps that distinction in the data instead of in a convention.
func (s *Store) CancellationTerms(
	ctx context.Context,
	q db.TX,
	ratePlanID string,
) ([]byte, error) {
	var terms []byte
	err := q.QueryRow(ctx, `
		select jsonb_build_object(
		           'policy', p.name,
		           'bands', jsonb_agg(jsonb_build_object(
		               'days_before_min', b.days_before_min,
		               'days_before_max', b.days_before_max,
		               'charge_pct',      b.charge_pct,
		               'fixed_fee_minor', b.fixed_fee_minor)
		               order by b.days_before_min))
		  from rate_plan rp
		  join cancellation_policy p on p.id = rp.cancellation_policy_id
		  join cancellation_band b on b.policy_id = p.id
		 where rp.id = $1
		 group by p.name`, ratePlanID).Scan(&terms)
	switch {
	case err == nil:
		return terms, nil
	case errors.Is(err, pgx.ErrNoRows):
		return nil, nil
	default:
		return nil, fmt.Errorf("read cancellation terms: %w", err)
	}
}

// InputHash covers exactly what the guest chose: the category, the dates, the
// party, the extras and the campaign code. Nothing the server derived is in it.
//
// That boundary is the whole freeze mechanism. Confirming a booking recomputes
// this from the request and compares it to the hash stored on the quote, so a
// changed party or a changed date is refused rather than quietly charged at the
// old total — and it can only be recomputed because the inputs are all things
// the caller still has.
func InputHash(categoryCode string, r Request) [32]byte {
	children := make([]string, 0, len(r.Children))
	for _, c := range r.Children {
		children = append(children, c.DateOfBirth)
	}
	return hashOf(struct {
		Category  string   `json:"category"`
		Arrival   string   `json:"arrival"`
		Departure string   `json:"departure"`
		Adults    int      `json:"adults"`
		Children  []string `json:"children"`
		Pets      int      `json:"pets"`
		Vehicles  int      `json:"vehicles"`
		Campaign  string   `json:"campaign"`
	}{
		categoryCode, r.Arrival, r.Departure, r.Adults, children,
		r.Pets, r.Vehicles, r.CampaignCode,
	})
}

// hashOf hashes the canonical JSON of a value. Go's encoder sorts map keys, and
// the engine emits slices in a fixed order, so the same inputs always hash the
// same — which is what makes the hash usable as the freeze check on confirm.
func hashOf(v any) [32]byte {
	encoded, err := json.Marshal(v)
	if err != nil {
		// Every type hashed here is a plain struct of scalars and slices; a
		// failure would be a programming error, not a runtime condition.
		panic(fmt.Sprintf("pricing: hash %T: %v", v, err))
	}
	return sha256.Sum256(encoded)
}
