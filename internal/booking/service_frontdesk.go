package booking

import (
	"context"
	"encoding/json"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/db"
	"github.com/jackc/pgx/v5"
)

// CheckIn marks the guest as arrived and pins the pitch they were assigned.
//
// Pinning is the point at which a provisional assignment becomes a commitment.
// Up to now the guest has never been told which pitch is theirs and staff could
// move them freely; from now on somebody has been sent to A17 and the number is
// on a piece of paper in their car.
//
// The transition is a guarded UPDATE rather than a read and then a write. Two
// receptionists checking in the same arrival at once is ordinary at eleven on
// a Saturday, and the WHERE clause is what makes the second one a no-op rather
// than a second event in the history.
func (s *Store) CheckIn(
	ctx context.Context,
	bookingID, actorUserID string,
) (Summary, error) {
	return s.transition(ctx, transition{
		bookingID:   bookingID,
		actorUserID: actorUserID,
		from:        StateConfirmed,
		to:          StateCheckedIn,
		pinUnit:     true,
		event:       EventCheckedIn,
	})
}

// CheckOut marks the stay finished. The unit stays pinned: which pitch somebody
// actually stayed on is a fact about the past, and unpinning it would make the
// history unreadable the moment the pitch is reassigned.
func (s *Store) CheckOut(
	ctx context.Context,
	bookingID, actorUserID string,
) (Summary, error) {
	return s.transition(ctx, transition{
		bookingID:   bookingID,
		actorUserID: actorUserID,
		from:        StateCheckedIn,
		to:          StateCheckedOut,
		event:       EventCheckedOut,
	})
}

// Cancel releases the pitch.
//
// No fee is computed and none is charged. The terms were frozen onto the
// booking at confirmation and are read back when payments exist to apply them
// to; computing a cancellation fee with nothing able to collect it would be a
// number on a screen that means nothing, and staff would reasonably assume it
// had been taken.
func (s *Store) Cancel(
	ctx context.Context,
	bookingID, actorUserID, reason string,
) (Summary, error) {
	return s.transition(ctx, transition{
		bookingID:   bookingID,
		actorUserID: actorUserID,
		from:        StateConfirmed,
		to:          StateCancelled,
		event:       EventCancelled,
		reason:      reason,
	})
}

type transition struct {
	bookingID   string
	actorUserID string
	from        string
	to          string
	pinUnit     bool
	event       string
	reason      string
}

// transition moves a booking's allocation from one state to the next.
//
// When the UPDATE matches nothing, a second read distinguishes "no such
// booking" from "not in that state". Collapsing the two into one error would
// leave reception unable to tell a mistyped reference from a guest a colleague
// has already checked in, which are opposite problems.
func (s *Store) transition(
	ctx context.Context,
	t transition,
) (Summary, error) {
	var out Summary

	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		var allocationID string
		err := tx.QueryRow(ctx, `
			update unit_allocation
			   set state = $3,
			       unit_pinned = unit_pinned or $4
			 where booking_id = $1 and state = $2
			returning id::text`,
			t.bookingID, t.from, t.to, t.pinUnit).Scan(&allocationID)
		switch {
		case err == nil:
		case isNoRows(err), db.IsBadInput(err):
			return s.explainRefusal(ctx, tx, t.bookingID)
		default:
			return wrap("transition allocation", err)
		}

		detail := []byte(nil)
		if t.reason != "" {
			detail, err = json.Marshal(map[string]string{"reason": t.reason})
			if err != nil {
				return wrap("encode event detail", err)
			}
		}
		err = s.event(
			ctx, tx, t.bookingID, t.event, "staff", &t.actorUserID, detail)
		if err != nil {
			return err
		}

		row := tx.QueryRow(ctx, summarySelect+" where b.id = $1", t.bookingID)
		out, err = scanSummary(row)
		if err != nil {
			return wrap("read booking after transition", err)
		}
		return nil
	})
	if err != nil {
		return Summary{}, err
	}
	return out, nil
}

// explainRefusal turns a zero-row UPDATE into the right error.
func (s *Store) explainRefusal(
	ctx context.Context,
	tx pgx.Tx,
	bookingID string,
) error {
	var exists bool
	err := tx.QueryRow(ctx,
		`select exists (select 1 from booking where id = $1)`, bookingID,
	).Scan(&exists)
	if err != nil {
		if db.IsBadInput(err) {
			return ErrNotFound
		}
		return wrap("check booking exists", err)
	}
	if !exists {
		return ErrNotFound
	}
	return ErrWrongState
}

// Reassign moves a booking to a different pitch.
//
// The exclusion constraint decides whether the move is legal, so there is no
// availability check here — asking first would be both a race and a second
// implementation of the same rule. A move onto an occupied pitch comes back as
// ErrNoUnitAvailable, which is what the caller can act on.
//
// Requirements are not re-checked. Staff who move a guest are looking at the
// booking's requirements on the same screen, and refusing a move reception has
// decided on would mean a guest standing at the desk while the system argues.
func (s *Store) Reassign(
	ctx context.Context,
	bookingID, unitID, actorUserID string,
) (Summary, error) {
	var out Summary

	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		var fromCode, toCode string
		// The old pitch has to be read in a CTE: a RETURNING clause sees the
		// row as it now is, so asking it where the guest used to be would name
		// where they are.
		err := tx.QueryRow(ctx, `
			with before as (
			    select unit_id from unit_allocation where booking_id = $1
			), moved as (
			    update unit_allocation a set unit_id = $2
			     where a.booking_id = $1
			       and a.state in ('confirmed', 'checked_in')
			       and exists (
			           select 1 from unit u
			            where u.id = $2 and u.category_id = a.category_id
			              and u.status = 'active')
			    returning a.id
			)
			select coalesce(
			           (select code from unit
			             where id = (select unit_id from before)), ''),
			       (select code from unit where id = $2)
			  from moved`,
			bookingID, unitID).Scan(&fromCode, &toCode)
		switch {
		case err == nil:
		case isOverlap(err):
			return ErrNoUnitAvailable
		case isNoRows(err), db.IsBadInput(err):
			return s.explainRefusal(ctx, tx, bookingID)
		default:
			return wrap("reassign allocation", err)
		}

		detail, err := json.Marshal(map[string]string{
			"from": fromCode, "to": toCode,
		})
		if err != nil {
			return wrap("encode event detail", err)
		}
		err = s.event(
			ctx, tx, bookingID, EventReassigned, "staff", &actorUserID, detail)
		if err != nil {
			return err
		}

		row := tx.QueryRow(ctx, summarySelect+" where b.id = $1", bookingID)
		out, err = scanSummary(row)
		if err != nil {
			return wrap("read booking after reassign", err)
		}
		return nil
	})
	if err != nil {
		return Summary{}, err
	}
	return out, nil
}
