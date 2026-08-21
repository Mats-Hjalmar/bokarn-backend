package outbox

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/logging"
	"github.com/Mats-Hjalmar/bokarn-backend/internal/tenant"
	"github.com/jackc/pgx/v5"
)

var logger = logging.New("outbox")

// BatchSize bounds one drain pass. Small enough that a slow handler cannot hold
// a transaction open across the whole queue, large enough that a burst of
// bookings clears in one tick.
const BatchSize = 20

// Dispatcher drains the outbox for one operator at a time.
type Dispatcher struct {
	db       *tenant.DB
	handlers map[string]Handler
}

// NewDispatcher constructs a Dispatcher. Handlers are registered up front in
// cmd/api so the wiring is one greppable place rather than a scatter of init
// functions.
func NewDispatcher(d *tenant.DB, handlers map[string]Handler) *Dispatcher {
	return &Dispatcher{db: d, handlers: handlers}
}

// Drain delivers up to BatchSize messages for the pinned operator and reports
// how many were delivered.
//
// Each message gets its own transaction. That is the point: the handler's proof
// of delivery and the row's delivered_at commit together, so a crash between
// them is impossible, and one message's permanent failure does not roll back
// the successful deliveries beside it.
func (d *Dispatcher) Drain(ctx context.Context) (int, error) {
	delivered := 0
	for range BatchSize {
		done, err := d.one(ctx)
		if err != nil {
			return delivered, err
		}
		if !done {
			return delivered, nil
		}
		delivered++
	}
	return delivered, nil
}

// one claims and delivers a single message, reporting whether there was one to
// claim.
//
// `for update skip locked` is what lets several replicas drain the same queue
// without coordinating: a row another process is already working on is
// invisible to this one rather than something to wait for.
func (d *Dispatcher) one(ctx context.Context) (bool, error) {
	claimed := false

	err := d.db.Tx(ctx, func(tx pgx.Tx) error {
		var m Pending
		err := tx.QueryRow(ctx, `
			select id::text, kind, payload, attempts
			  from outbox_message
			 where delivered_at is null and available_at <= now()
			   and attempts < $1
			 order by available_at
			 limit 1
			 for update skip locked`, MaxAttempts).
			Scan(&m.ID, &m.Kind, &m.Payload, &m.Attempts)
		if err != nil {
			if errorsIsNoRows(err) {
				return nil
			}
			return fmt.Errorf("claim message: %w", err)
		}
		claimed = true

		handler, ok := d.handlers[m.Kind]
		if !ok {
			return d.fail(ctx, tx, m, ErrNoHandler)
		}

		if err := handler(ctx, tx, m); err != nil {
			return d.fail(ctx, tx, m, err)
		}

		_, err = tx.Exec(
			ctx,
			`update outbox_message set delivered_at = now(), attempts = attempts + 1
			  where id = $1`,
			m.ID,
		)
		if err != nil {
			return fmt.Errorf("mark delivered: %w", err)
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return claimed, nil
}

// fail records the failure and schedules a retry.
//
// It returns nil so the surrounding transaction commits: the attempt counter
// and the error text are the only things worth keeping from a failed delivery,
// and rolling them back would lose the record of the failure along with it,
// leaving the message to be retried immediately and forever.
func (d *Dispatcher) fail(
	ctx context.Context,
	tx pgx.Tx,
	m Pending,
	cause error,
) error {
	attempts := m.Attempts + 1
	_, err := tx.Exec(ctx, `
		update outbox_message
		   set attempts = $2, last_error = $3, available_at = now() + $4
		 where id = $1`, m.ID, attempts, cause.Error(), backoff(attempts))
	if err != nil {
		return fmt.Errorf("record failure: %w", err)
	}

	level := slog.LevelWarn
	if attempts >= MaxAttempts {
		level = slog.LevelError
	}
	logger.Log(ctx, level, "outbox delivery failed",
		"kind", m.Kind, "message", m.ID, "attempts", attempts,
		"exhausted", attempts >= MaxAttempts, "err", cause)
	return nil
}
