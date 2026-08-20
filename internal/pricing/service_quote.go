package pricing

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

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
	Arrival       string `json:"arrival"`
	Departure     string `json:"departure"`
	ExpiresAt     string `json:"expires_at"`
	InputHash     string `json:"input_hash"`
	BreakdownHash string `json:"breakdown_hash"`
	Quote
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

	req.LeadDays = daysBetween(now.Format(time.DateOnly), req.Arrival)
	if req.LeadDays < 0 {
		req.LeadDays = 0
	}

	occupancy, resolvedSite, categoryID, err := s.categoryOccupancy(
		ctx, siteID, categoryCode, req.Arrival, req.Departure)
	if err != nil {
		return StoredQuote{}, err
	}
	req.OccupancyBP = occupancy

	q, err := Price(snap, req)
	if err != nil {
		return StoredQuote{}, err
	}

	inputHash := hashOf(struct {
		Category string  `json:"category"`
		Req      Request `json:"request"`
	}{categoryCode, req})
	breakdownHash := hashOf(q)

	payload, err := json.Marshal(q)
	if err != nil {
		return StoredQuote{}, fmt.Errorf("encode quote: %w", err)
	}

	expires := now.Add(QuoteTTL)
	out := StoredQuote{
		CategoryCode:  categoryCode,
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
) (occupancyBP int, resolvedSite, categoryID string, err error) {
	err = s.db.ReadTx(ctx, func(tx pgx.Tx) error {
		var total, taken int
		err := tx.QueryRow(ctx, `
			select c.site_id::text, c.id::text,
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
			 group by c.site_id, c.id`, categoryCode, arrival, departure, siteID,
		).Scan(&resolvedSite, &categoryID, &total, &taken)
		if err != nil {
			return fmt.Errorf("read category occupancy: %w", err)
		}
		if total > 0 {
			occupancyBP = taken * 10000 / total
		}
		return nil
	})
	return occupancyBP, resolvedSite, categoryID, err
}

// LoadQuote reloads a stored quote for the confirm path.
func (s *Store) LoadQuote(
	ctx context.Context,
	id string,
) (StoredQuote, error) {
	var out StoredQuote
	var payload []byte
	var inputHash, breakdownHash []byte
	var expires time.Time

	err := s.db.ReadTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			select q.id::text, c.code, q.arrival::text, q.departure::text,
			       q.expires_at, q.input_hash, q.breakdown_hash, q.payload
			  from quote q
			  join unit_category c on c.id = q.category_id
			 where q.id = $1`, id,
		).Scan(&out.ID, &out.CategoryCode, &out.Arrival, &out.Departure,
			&expires, &inputHash, &breakdownHash, &payload)
	})
	if err != nil {
		return StoredQuote{}, fmt.Errorf("load quote: %w", err)
	}

	if err := json.Unmarshal(payload, &out.Quote); err != nil {
		return StoredQuote{}, fmt.Errorf("decode quote payload: %w", err)
	}
	out.ExpiresAt = expires.Format(time.RFC3339)
	out.InputHash = fmt.Sprintf("%x", inputHash)
	out.BreakdownHash = fmt.Sprintf("%x", breakdownHash)
	return out, nil
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
