package booking

import (
	"context"
	"encoding/json"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/db"
	"github.com/jackc/pgx/v5"
)

// Summary is one booking as a list shows it. The unit is included because
// reception's first question about a booking is which pitch it is on, and the
// second is who is on it.
type Summary struct {
	ID           string `json:"id"`
	Reference    string `json:"reference"`
	GuestName    string `json:"guest_name"`
	Email        string `json:"email"`
	Phone        string `json:"phone"`
	CategoryCode string `json:"category_code"`
	CategoryName string `json:"category_name"`
	UnitCode     string `json:"unit_code"`
	UnitPinned   bool   `json:"unit_pinned"`
	Arrival      string `json:"arrival"`
	Departure    string `json:"departure"`
	Nights       int    `json:"nights"`
	State        string `json:"state"`
	Adults       int    `json:"adults"`
	Children     int    `json:"children"`
	Pets         int    `json:"pets"`
	Currency     string `json:"currency"`
	TotalGross   int64  `json:"total_gross_minor"`
	Channel      string `json:"channel"`
	ConfirmedAt  string `json:"confirmed_at"`
}

// PriceLine is one frozen row of the breakdown.
type PriceLine struct {
	Seq            int    `json:"seq"`
	Kind           string `json:"kind"`
	StayDate       string `json:"stay_date,omitempty"`
	Description    string `json:"description"`
	Qty            int    `json:"qty"`
	UnitGrossMinor int64  `json:"unit_gross_minor"`
	GrossMinor     int64  `json:"gross_minor"`
	NetMinor       int64  `json:"net_minor"`
	VATMinor       int64  `json:"vat_minor"`
	VATCode        string `json:"vat_code"`
	VATRateBP      int    `json:"vat_rate_bp"`
}

// Event is one entry from the booking's history. Detail is RawMessage rather
// than []byte so the stored jsonb reaches the client as the object it is; a
// []byte would arrive base64-encoded.
type Event struct {
	Kind      string          `json:"kind"`
	Actor     string          `json:"actor"`
	ActorName string          `json:"actor_name,omitempty"`
	Detail    json.RawMessage `json:"detail,omitempty"`
	CreatedAt string          `json:"created_at"`
}

// Requirement is something this stay needs from a pitch.
type Requirement struct {
	AttrKey  string `json:"attr_key"`
	Op       string `json:"op"`
	Value    string `json:"value"`
	Required bool   `json:"required"`
}

// Detail is a booking with everything hanging off it.
type Detail struct {
	Summary
	CountryOfResidence string        `json:"country_of_residence"`
	Locale             string        `json:"locale"`
	Notes              string        `json:"notes,omitempty"`
	TotalNet           int64         `json:"total_net_minor"`
	TotalVAT           int64         `json:"total_vat_minor"`
	Lines              []PriceLine   `json:"lines"`
	Events             []Event       `json:"events"`
	Requirements       []Requirement `json:"requirements"`
}

// summarySelect is spelled out rather than shared, per the house rule, but the
// list and the detail read the same shape and a second copy would be a second
// thing to keep in step for no benefit.
const summarySelect = `
	select b.id::text, b.reference,
	       g.given_names, g.surname, g.email, g.phone,
	       cat.code, cat.name,
	       coalesce(u.code, ''), a.unit_pinned,
	       a.arrival_date::text, a.departure_date::text,
	       a.departure_date - a.arrival_date, a.state,
	       a.adults, a.children, a.pets,
	       b.currency, b.total_gross_minor, b.channel,
	       b.confirmed_at::text
	  from booking b
	  join unit_allocation a on a.booking_id = b.id
	  join unit_category cat on cat.id = b.category_id
	  join guest_identity g on g.id = b.guest_id
	  left join unit u on u.id = a.unit_id`

func scanSummary(row pgx.Row) (Summary, error) {
	var s Summary
	var given, surname string
	err := row.Scan(&s.ID, &s.Reference, &given, &surname, &s.Email, &s.Phone,
		&s.CategoryCode, &s.CategoryName, &s.UnitCode, &s.UnitPinned,
		&s.Arrival, &s.Departure, &s.Nights, &s.State,
		&s.Adults, &s.Children, &s.Pets,
		&s.Currency, &s.TotalGross, &s.Channel, &s.ConfirmedAt)
	if err != nil {
		return Summary{}, err
	}
	s.GuestName = surname
	if given != "" {
		s.GuestName = surname + ", " + given
	}
	return s, nil
}

// ListFilter narrows the booking list.
type ListFilter struct {
	// State empty means every state, including cancelled — the list is a
	// record, not a work queue, and a cancelled booking is exactly what somebody
	// searching for a reference is often looking for.
	State string
	// Search matches a reference, a surname or an email.
	Search string
	Limit  int
}

// List returns bookings, newest confirmation first.
func (s *Store) List(
	ctx context.Context,
	f ListFilter,
) ([]Summary, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 100
	}

	out := []Summary{}
	err := s.db.ReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, summarySelect+`
			 where ($1 = '' or a.state = $1)
			   and ($2 = '' or b.reference ilike '%' || $2 || '%'
			                or g.surname ilike '%' || $2 || '%'
			                or g.email ilike '%' || $2 || '%')
			 order by b.confirmed_at desc
			 limit $3`, f.State, f.Search, f.Limit)
		if err != nil {
			return wrap("query bookings", err)
		}
		defer rows.Close()

		for rows.Next() {
			item, err := scanSummary(rows)
			if err != nil {
				return wrap("scan booking", err)
			}
			out = append(out, item)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Frontdesk is a day's arrivals, departures and in-house stays.
type Frontdesk struct {
	Date       string    `json:"date"`
	Arrivals   []Summary `json:"arrivals"`
	Departures []Summary `json:"departures"`
	InHouse    []Summary `json:"in_house"`
}

// Day returns the three lists reception works from.
//
// Arrivals are still confirmed, departures are already checked in, and in-house
// is everyone whose stay covers the date and who has arrived. The state
// conditions are what make each list a work queue: a guest who has checked in
// leaves the arrivals list by having done so, not by anyone marking it.
func (s *Store) Day(ctx context.Context, date string) (Frontdesk, error) {
	out := Frontdesk{
		Date:       date,
		Arrivals:   []Summary{},
		Departures: []Summary{},
		InHouse:    []Summary{},
	}

	err := s.db.ReadTx(ctx, func(tx pgx.Tx) error {
		lists := []struct {
			where string
			into  *[]Summary
		}{
			{`a.arrival_date = $1::date and a.state = 'confirmed'`,
				&out.Arrivals},
			{`a.departure_date = $1::date and a.state = 'checked_in'`,
				&out.Departures},
			{`a.state = 'checked_in' and a.stay @> $1::date`, &out.InHouse},
		}

		for _, list := range lists {
			rows, err := tx.Query(ctx,
				summarySelect+" where "+list.where+
					" order by cat.sort_order, u.sort_order, g.surname", date)
			if err != nil {
				return wrap("query front desk list", err)
			}
			for rows.Next() {
				item, err := scanSummary(rows)
				if err != nil {
					rows.Close()
					return wrap("scan front desk row", err)
				}
				*list.into = append(*list.into, item)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Frontdesk{}, err
	}
	return out, nil
}

// Detail loads one booking by id, for staff.
func (s *Store) Detail(ctx context.Context, id string) (Detail, error) {
	return s.detail(ctx, `b.id = $1`, id)
}

// ByReference loads one booking for a guest presenting the token from their
// email.
//
// The token is looked up by hash and the reference has to match the same row,
// so a valid token for one booking cannot read another. Both are required: the
// reference alone is guessable and is meant to be quotable over the phone.
func (s *Store) ByReference(
	ctx context.Context,
	reference, token string,
) (Detail, error) {
	d, err := s.detail(ctx, `
		b.reference = $1 and exists (
		    select 1 from booking_access_token t
		     where t.booking_id = b.id
		       and t.token_hash = $2
		       and t.expires_at > now())`, reference, HashAccessToken(token))
	if err != nil {
		return Detail{}, err
	}

	// The pitch number is withheld until the guest arrives on it. Assignment is
	// provisional right up to check-in — staff move people around freely — so
	// showing a number now would be a promise the system has not made, and a
	// guest who has read "A17" in an email will go and park on A17.
	switch d.State {
	case StateCheckedIn, StateCheckedOut:
	default:
		d.UnitCode = ""
		d.UnitPinned = false
	}
	return d, nil
}

func (s *Store) detail(
	ctx context.Context,
	where string,
	args ...any,
) (Detail, error) {
	var d Detail

	err := s.db.ReadTx(ctx, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, summarySelect+" where "+where, args...)
		summary, err := scanSummary(row)
		switch {
		case err == nil:
		case isNoRows(err), db.IsBadInput(err):
			return ErrNotFound
		default:
			return wrap("read booking", err)
		}
		d.Summary = summary

		err = tx.QueryRow(ctx, `
			select g.country_of_residence, b.locale, coalesce(b.notes, ''),
			       b.total_net_minor, b.total_vat_minor
			  from booking b join guest_identity g on g.id = b.guest_id
			 where b.id = $1`, d.ID,
		).Scan(&d.CountryOfResidence, &d.Locale, &d.Notes,
			&d.TotalNet, &d.TotalVAT)
		if err != nil {
			return wrap("read booking detail", err)
		}

		if d.Lines, err = readLines(ctx, tx, d.ID); err != nil {
			return err
		}
		if d.Events, err = readEvents(ctx, tx, d.ID); err != nil {
			return err
		}
		d.Requirements, err = readRequirements(ctx, tx, d.ID)
		return err
	})
	if err != nil {
		return Detail{}, err
	}
	return d, nil
}

// readLines returns the frozen breakdown for the latest amendment.
//
// v1 never amends, so there is exactly one amendment_id per booking and the
// ordering is decorative. It is written this way because the day an amendment
// is added, a query reading every amendment's lines would silently show a guest
// both the old price and the new one.
func readLines(
	ctx context.Context,
	tx pgx.Tx,
	bookingID string,
) ([]PriceLine, error) {
	rows, err := tx.Query(ctx, `
		select seq, kind, coalesce(stay_date::text, ''), description, qty,
		       unit_gross_minor, gross_minor, net_minor, vat_minor,
		       vat_code, vat_rate_bp
		  from booking_price_line
		 where booking_id = $1
		   and amendment_id = (
		       select amendment_id from booking_price_line
		        where booking_id = $1 order by created_at desc limit 1)
		 order by seq`, bookingID)
	if err != nil {
		return nil, wrap("query price lines", err)
	}
	defer rows.Close()

	out := []PriceLine{}
	for rows.Next() {
		var l PriceLine
		if err := rows.Scan(&l.Seq, &l.Kind, &l.StayDate, &l.Description,
			&l.Qty, &l.UnitGrossMinor, &l.GrossMinor, &l.NetMinor,
			&l.VATMinor, &l.VATCode, &l.VATRateBP); err != nil {
			return nil, wrap("scan price line", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func readEvents(
	ctx context.Context,
	tx pgx.Tx,
	bookingID string,
) ([]Event, error) {
	rows, err := tx.Query(ctx, `
		select e.kind, e.actor,
		       coalesce(nullif(u.name, ''), u.email, ''), e.detail,
		       e.created_at::text
		  from reservation_event e
		  left join users u on u.id = e.actor_user_id
		 where e.booking_id = $1
		 order by e.created_at`, bookingID)
	if err != nil {
		return nil, wrap("query events", err)
	}
	defer rows.Close()

	out := []Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(
			&e.Kind, &e.Actor, &e.ActorName, &e.Detail, &e.CreatedAt,
		); err != nil {
			return nil, wrap("scan event", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func readRequirements(
	ctx context.Context,
	tx pgx.Tx,
	bookingID string,
) ([]Requirement, error) {
	rows, err := tx.Query(ctx, `
		select attr_key, op, value, required
		  from allocation_requirement
		 where booking_id = $1 order by attr_key`, bookingID)
	if err != nil {
		return nil, wrap("query requirements", err)
	}
	defer rows.Close()

	out := []Requirement{}
	for rows.Next() {
		var r Requirement
		if err := rows.Scan(
			&r.AttrKey,
			&r.Op,
			&r.Value,
			&r.Required,
		); err != nil {
			return nil, wrap("scan requirement", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
