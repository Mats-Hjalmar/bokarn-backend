package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/db"
	"github.com/jackc/pgx/v5"
)

// insertGuest creates a guest identity inside the caller's pinned transaction.
func insertGuest(t *testing.T, tx pgx.Tx, email string) string {
	t.Helper()
	var id string
	err := tx.QueryRow(t.Context(), `
		insert into guest_identity
		    (given_names, surname, email, phone, country_of_residence,
		     purge_after)
		values ('Test', 'Person', $1, '+46700000000', 'SE',
		        current_date + 100)
		returning id::text`, email).Scan(&id)
	if err != nil {
		t.Fatalf("insert guest: %v", err)
	}
	return id
}

// insertQuote creates the minimal quote a booking must reference.
func insertQuote(t *testing.T, tx pgx.Tx, unit string) string {
	t.Helper()
	var id string
	err := tx.QueryRow(t.Context(), `
		insert into quote
		    (site_id, category_id, rate_plan_id, arrival, departure, currency,
		     engine_version, input_hash, breakdown_hash, payload,
		     total_gross_minor, total_net_minor, total_vat_minor, expires_at)
		select u.site_id, u.category_id, p.id,
		       current_date + 400, current_date + 403, 'SEK', 1,
		       '\x00'::bytea, '\x00'::bytea, '{}'::jsonb,
		       112, 100, 12, now() + interval '30 minutes'
		  from unit u
		  join rate_plan p on p.category_id = u.category_id
		 where u.id = $1
		 limit 1
		returning id::text`, unit).Scan(&id)
	if err != nil {
		t.Fatalf("insert quote: %v", err)
	}
	return id
}

// far is a date well beyond every seeded booking, so a test's window cannot
// collide with the demo data. Computed in Go rather than passed as an SQL
// expression: a parameter is a value, and "current_date + 500" reaches Postgres
// as the string it is.
func far(days int) string {
	return time.Now().AddDate(0, 0, 500+days).Format(time.DateOnly)
}

// insertBooking creates a booking and its occupancy row on one unit.
func insertBooking(
	t *testing.T,
	tx pgx.Tx,
	unit, reference, from, to string,
) (bookingID string) {
	t.Helper()

	guest := insertGuest(t, tx, reference+"@example.se")
	quote := insertQuote(t, tx, unit)

	err := tx.QueryRow(t.Context(), `
		insert into booking
		    (reference, site_id, category_id, guest_id, quote_id,
		     engine_version, input_hash, quote_hash, idempotency_key,
		     currency, total_gross_minor, total_net_minor, total_vat_minor,
		     locale, channel)
		select $2, u.site_id, u.category_id, $3, $4, 1,
		       '\x00'::bytea, '\x00'::bytea, $2, 'SEK', 112, 100, 12,
		       'sv', 'web'
		  from unit u where u.id = $1
		returning id::text`, unit, reference, guest, quote).Scan(&bookingID)
	if err != nil {
		t.Fatalf("insert booking: %v", err)
	}

	_, err = tx.Exec(t.Context(), `
		insert into unit_allocation
		    (site_id, category_id, unit_id, booking_id, kind, state, stay,
		     adults)
		select u.site_id, u.category_id, u.id, $2, 'booking', 'confirmed',
		       daterange($3::date, $4::date), 2
		  from unit u where u.id = $1`, unit, bookingID, from, to)
	if err != nil {
		t.Fatalf("insert booking allocation: %v", err)
	}
	return bookingID
}

// A hold is anonymous occupancy: it is taken while the guest is still typing
// their name, so it cannot carry a booking id. The original CHECK required one
// for anything that was not a block, which made holds impossible.
func TestHoldsCarryNoBooking(t *testing.T) {
	tx := begin(t, storsand)
	unit := unitID(t, tx, "A21")

	_, err := tx.Exec(t.Context(), `
		insert into unit_allocation
		    (site_id, category_id, unit_id, kind, state, stay, expires_at)
		select u.site_id, u.category_id, u.id, 'hold', 'held',
		       daterange($2::date, $3::date), now() + interval '15 minutes'
		  from unit u where u.id = $1`, unit, far(0), far(3))
	if err != nil {
		t.Fatalf("a hold with no booking must be allowed: %v", err)
	}
}

// A hold without a deadline would never be released by either half of expiry,
// so the pitch would be occupied by nobody forever.
func TestHoldsMustHaveADeadline(t *testing.T) {
	tx := begin(t, storsand)
	unit := unitID(t, tx, "A22")

	_, err := tx.Exec(t.Context(), `
		insert into unit_allocation
		    (site_id, category_id, unit_id, kind, state, stay)
		select u.site_id, u.category_id, u.id, 'hold', 'held',
		       daterange($2::date, $3::date)
		  from unit u where u.id = $1`, unit, far(0), far(3))
	if sqlState(err) != db.CheckViolation {
		t.Fatalf("want a check violation, got %v", err)
	}
}

// A booking must name a guest. The composite foreign key is what makes that
// true across operators as well: pointing at another operator's guest has to
// fail, and a single-column key would let it succeed.
func TestBookingCannotNameAnotherOperatorsGuest(t *testing.T) {
	other := begin(t, hamnviken)
	foreign := insertGuest(t, other, "foreign-guest@example.se")

	tx := begin(t, storsand)
	unit := unitID(t, tx, "A23")
	quote := insertQuote(t, tx, unit)

	_, err := tx.Exec(t.Context(), `
		insert into booking
		    (reference, site_id, category_id, guest_id, quote_id,
		     engine_version, input_hash, quote_hash, idempotency_key,
		     currency, total_gross_minor, total_net_minor, total_vat_minor,
		     locale, channel)
		select 'XGUEST', u.site_id, u.category_id, $2, $3, 1,
		       '\x00'::bytea, '\x00'::bytea, 'XGUEST', 'SEK', 112, 100, 12,
		       'sv', 'web'
		  from unit u where u.id = $1`, unit, foreign, quote)
	if err == nil {
		t.Fatal("a booking named another operator's guest and was accepted")
	}
}

// The freeze is only as good as the immutability under it. A price line that
// can be edited is a price nobody can be held to.
func TestPriceLinesCannotBeRewritten(t *testing.T) {
	tx := begin(t, storsand)
	unit := unitID(t, tx, "A24")
	booking := insertBooking(t, tx, unit, "FROZEN",
		far(0), far(3))

	_, err := tx.Exec(t.Context(), `
		insert into booking_price_line
		    (booking_id, amendment_id, seq, kind, description, qty,
		     unit_gross_minor, gross_minor, net_minor, vat_minor,
		     vat_code, vat_rate_bp, vat_treatment)
		values ($1, gen_random_uuid(), 1, 'accommodation', 'Natt', 1,
		        112, 112, 100, 12, 'SE12', 1200, 'standard')`, booking)
	if err != nil {
		t.Fatalf("insert price line: %v", err)
	}

	_, err = tx.Exec(t.Context(),
		`update booking_price_line set gross_minor = 0 where booking_id = $1`,
		booking)
	if !mentionsAppendOnly(err) {
		t.Fatalf("want an append-only refusal on update, got %v", err)
	}
}

// A line may not be erased on its own, because that changes the total just as
// surely as editing one.
func TestPriceLinesCannotBeDeletedAlone(t *testing.T) {
	tx := begin(t, storsand)
	unit := unitID(t, tx, "A25")
	booking := insertBooking(t, tx, unit, "ALONE",
		far(0), far(3))

	_, err := tx.Exec(t.Context(), `
		insert into booking_price_line
		    (booking_id, amendment_id, seq, kind, description, qty,
		     unit_gross_minor, gross_minor, net_minor, vat_minor,
		     vat_code, vat_rate_bp, vat_treatment)
		values ($1, gen_random_uuid(), 1, 'accommodation', 'Natt', 1,
		        112, 112, 100, 12, 'SE12', 1200, 'standard')`, booking)
	if err != nil {
		t.Fatalf("insert price line: %v", err)
	}

	_, err = tx.Exec(t.Context(),
		`delete from booking_price_line where booking_id = $1`, booking)
	if err == nil {
		t.Fatal("a price line was deleted on its own")
	}
}

// And yet a booking must remain deletable, or the retention purge cannot erase
// a guest's data when its years are up. Append-only has to mean "cannot be
// rewritten", not "cannot ever be erased".
func TestDeletingABookingTakesItsLinesAndEvents(t *testing.T) {
	tx := begin(t, storsand)
	unit := unitID(t, tx, "A26")
	booking := insertBooking(t, tx, unit, "PURGE",
		far(0), far(3))

	_, err := tx.Exec(t.Context(), `
		insert into booking_price_line
		    (booking_id, amendment_id, seq, kind, description, qty,
		     unit_gross_minor, gross_minor, net_minor, vat_minor,
		     vat_code, vat_rate_bp, vat_treatment)
		values ($1, gen_random_uuid(), 1, 'accommodation', 'Natt', 1,
		        112, 112, 100, 12, 'SE12', 1200, 'standard')`, booking)
	if err != nil {
		t.Fatalf("insert price line: %v", err)
	}
	_, err = tx.Exec(t.Context(),
		`insert into reservation_event (booking_id, kind, actor)
		 values ($1, 'confirmed', 'guest')`, booking)
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}

	// The occupancy row holds the booking with ON DELETE RESTRICT, so erasing a
	// stay is a deliberate two-step act rather than something a stray DELETE can
	// do by accident.
	_, err = tx.Exec(t.Context(),
		`delete from unit_allocation where booking_id = $1`, booking)
	if err != nil {
		t.Fatalf("delete allocation: %v", err)
	}
	if _, err := tx.Exec(t.Context(),
		`delete from booking where id = $1`, booking); err != nil {
		t.Fatalf("delete booking: %v", err)
	}

	var lines, events int
	err = tx.QueryRow(t.Context(), `
		select (select count(*) from booking_price_line where booking_id = $1),
		       (select count(*) from reservation_event where booking_id = $1)`,
		booking).Scan(&lines, &events)
	if err != nil {
		t.Fatalf("count leftovers: %v", err)
	}
	if lines != 0 || events != 0 {
		t.Fatalf("cascade left %d lines and %d events behind", lines, events)
	}
}

// Occupancy holds the booking: a stay cannot be erased while somebody is still
// recorded as being on a pitch for it.
func TestABookingWithOccupancyCannotBeDeleted(t *testing.T) {
	tx := begin(t, storsand)
	unit := unitID(t, tx, "A27")
	booking := insertBooking(t, tx, unit, "HELD",
		far(0), far(3))

	_, err := tx.Exec(t.Context(),
		`delete from booking where id = $1`, booking)
	if err == nil {
		t.Fatal("a booking with a live allocation was deleted")
	}
}

// One allocation per booking. It is what makes "the state of this booking" a
// single unambiguous row rather than a question about which row to believe.
func TestABookingCannotOccupyTwoPitches(t *testing.T) {
	tx := begin(t, storsand)
	first := unitID(t, tx, "A28")
	second := unitID(t, tx, "A29")
	booking := insertBooking(t, tx, first, "TWICE",
		far(0), far(3))

	_, err := tx.Exec(t.Context(), `
		insert into unit_allocation
		    (site_id, category_id, unit_id, booking_id, kind, state, stay)
		select u.site_id, u.category_id, u.id, $2, 'booking', 'confirmed',
		       daterange($3::date, $4::date)
		  from unit u where u.id = $1`, second, booking, far(0), far(3))
	if sqlState(err) != db.UniqueViolation {
		t.Fatalf("want a unique violation, got %v", err)
	}
}

// A reference is read out over the phone, so it has to identify one stay — but
// only within the operator, because two campsites are free to mint the same
// six characters.
func TestReferencesAreUniquePerOperatorAndNotGlobally(t *testing.T) {
	stor := begin(t, storsand)
	insertBooking(t, stor, unitID(t, stor, "A30"), "SHARED",
		far(0), far(3))

	_, err := stor.Exec(t.Context(), `
		insert into booking
		    (reference, site_id, category_id, guest_id, quote_id,
		     engine_version, input_hash, quote_hash, idempotency_key,
		     currency, total_gross_minor, total_net_minor, total_vat_minor,
		     locale, channel)
		select 'SHARED', b.site_id, b.category_id, b.guest_id, b.quote_id, 1,
		       '\x00'::bytea, '\x00'::bytea, 'other-key', 'SEK', 112, 100, 12,
		       'sv', 'web'
		  from booking b where b.reference = 'SHARED'`)
	if sqlState(err) != db.UniqueViolation {
		t.Fatalf("a reference was reused within one operator: %v", err)
	}

	other := begin(t, hamnviken)
	unit := unitID(t, other, "H01")
	insertBooking(t, other, unit, "SHARED",
		far(0), far(3))
}

// The same guard for idempotency keys, which is what makes a double-tapped
// Boka button return the first booking rather than make a second.
func TestIdempotencyKeysAreUniquePerOperator(t *testing.T) {
	tx := begin(t, storsand)
	insertBooking(t, tx, unitID(t, tx, "A31"), "IDEMP1",
		far(0), far(3))

	_, err := tx.Exec(t.Context(), `
		insert into booking
		    (reference, site_id, category_id, guest_id, quote_id,
		     engine_version, input_hash, quote_hash, idempotency_key,
		     currency, total_gross_minor, total_net_minor, total_vat_minor,
		     locale, channel)
		select 'IDEMP2', b.site_id, b.category_id, b.guest_id, b.quote_id, 1,
		       '\x00'::bytea, '\x00'::bytea, 'IDEMP1', 'SEK', 112, 100, 12,
		       'sv', 'web'
		  from booking b where b.reference = 'IDEMP1'`)
	if sqlState(err) != db.UniqueViolation {
		t.Fatalf("an idempotency key was reused: %v", err)
	}
}

// One person, two campsites, two guest records — because erasure at one must
// not reach into the other.
func TestGuestEmailsAreUniquePerOperatorAndNotGlobally(t *testing.T) {
	stor := begin(t, storsand)
	insertGuest(t, stor, "same.person@example.se")

	_, err := stor.Exec(t.Context(), `
		insert into guest_identity
		    (given_names, surname, email, phone, country_of_residence,
		     purge_after)
		values ('Test', 'Person', 'SAME.PERSON@example.se', '+46700000000',
		        'SE', current_date + 100)`)
	if sqlState(err) != db.UniqueViolation {
		t.Fatalf(
			"the same address was accepted twice for one operator: %v",
			err,
		)
	}

	other := begin(t, hamnviken)
	insertGuest(t, other, "same.person@example.se")
}

// A guest with no erasure date reads as "keep forever", which is the one answer
// data protection law does not allow.
func TestAGuestMustHaveAnErasureDate(t *testing.T) {
	tx := begin(t, storsand)

	_, err := tx.Exec(t.Context(), `
		insert into guest_identity
		    (given_names, surname, email, phone, country_of_residence)
		values ('Test', 'Person', 'no-purge@example.se', '+46700000000', 'SE')`)
	if sqlState(err) != db.NotNullViolation {
		t.Fatalf("want a not-null violation on purge_after, got %v", err)
	}
}

// An access token is a credential for reading a stranger's name, dates and
// total, so a dump of the table must be useless on its own.
func TestAccessTokensAreStoredHashedAndScopedToTheOperator(t *testing.T) {
	tx := begin(t, storsand)
	unit := unitID(t, tx, "A32")
	booking := insertBooking(t, tx, unit, "TOKENS",
		far(0), far(3))

	_, err := tx.Exec(t.Context(), `
		insert into booking_access_token (booking_id, token_hash, expires_at)
		values ($1, sha256('a-token'::bytea), now() + interval '30 days')`,
		booking)
	if err != nil {
		t.Fatalf("insert token: %v", err)
	}

	var stored []byte
	err = tx.QueryRow(t.Context(),
		`select token_hash from booking_access_token where booking_id = $1`,
		booking).Scan(&stored)
	if err != nil {
		t.Fatalf("read token: %v", err)
	}
	if len(stored) != 32 {
		t.Fatalf("stored credential is %d bytes, not a sha256 digest",
			len(stored))
	}
	if strings.Contains(string(stored), "a-token") {
		t.Fatal("the token itself was stored")
	}

	// Scoped to this booking's token, not to the table: the other operator has
	// tokens of its own and is entitled to see those. What must be invisible is
	// this one.
	other := begin(t, hamnviken)
	var visible int
	err = other.QueryRow(t.Context(),
		`select count(*) from booking_access_token
		  where token_hash = sha256('a-token'::bytea)`).Scan(&visible)
	if err != nil {
		t.Fatalf("count tokens as the other operator: %v", err)
	}
	if visible != 0 {
		t.Fatalf("another operator can see this booking's access token")
	}
}

// An event that says a staff member did something has to say which one, or the
// history answers nothing.
func TestStaffEventsMustNameTheirActor(t *testing.T) {
	tx := begin(t, storsand)
	unit := unitID(t, tx, "A33")
	booking := insertBooking(t, tx, unit, "ACTOR",
		far(0), far(3))

	_, err := tx.Exec(t.Context(),
		`insert into reservation_event (booking_id, kind, actor)
		 values ($1, 'checked_in', 'staff')`, booking)
	if sqlState(err) != db.CheckViolation {
		t.Fatalf("want a check violation for an anonymous staff event, got %v",
			err)
	}
}

// Exactly-once email rests on this index and nothing else: the outbox delivers
// at least once, so the second attempt after a crash has to collide here.
func TestOneMessagePerOutboxMessage(t *testing.T) {
	tx := begin(t, storsand)
	unit := unitID(t, tx, "A34")
	booking := insertBooking(t, tx, unit, "ONCE",
		far(0), far(3))

	var outboxID string
	err := tx.QueryRow(t.Context(), `
		insert into outbox_message (kind, payload, idempotency_key)
		values ('booking.confirmed', '{}'::jsonb, $1)
		returning id::text`, booking).Scan(&outboxID)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	send := func() error {
		_, err := tx.Exec(t.Context(), `
			insert into message_log
			    (channel, to_address, template_key, locale, booking_id,
			     outbox_message_id, subject)
			values ('email', 'a@b.se', 'booking_confirmed', 'sv', $1, $2,
			        'Din bokning')`, booking, outboxID)
		return err
	}

	if err := send(); err != nil {
		t.Fatalf("first send: %v", err)
	}
	if err := send(); sqlState(err) != db.UniqueViolation {
		t.Fatalf("a second delivery of the same message was recorded: %v", err)
	}
}

// A guest's own booking must not be readable through another operator's
// hostname, however the reference was obtained.
func TestBookingsAreTenantIsolated(t *testing.T) {
	stor := begin(t, storsand)
	insertBooking(t, stor, unitID(t, stor, "A35"), "ISOLAT",
		far(0), far(3))

	other := begin(t, hamnviken)
	var visible int
	err := other.QueryRow(t.Context(),
		`select count(*) from booking where reference = 'ISOLAT'`,
	).Scan(&visible)
	if err != nil {
		t.Fatalf("count bookings as the other operator: %v", err)
	}
	if visible != 0 {
		t.Fatalf("another operator can see %d of these bookings", visible)
	}
}

func mentionsAppendOnly(err error) bool {
	return err != nil && strings.Contains(err.Error(), "append-only")
}
