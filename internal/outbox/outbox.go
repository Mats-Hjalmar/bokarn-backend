// Package outbox makes an external side effect as durable as the state change
// that caused it.
//
// A message is written in the same transaction as the change it describes, so a
// booking cannot commit without its confirmation email being owed, and a
// rolled-back booking cannot leave one behind. Nothing here sends anything; the
// dispatcher drains the table and hands each message to a registered handler.
//
// Delivery is at-least-once, and deliberately so: the alternative is a
// distributed transaction with an SMTP server. Handlers must therefore be
// idempotent, and the mechanism for that is a unique row written in the same
// transaction as the send.
package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/db"
	"github.com/jackc/pgx/v5"
)

// MaxAttempts is how many times a message is retried before it is left alone
// for an operator to look at. It is not deleted and not marked delivered: a
// message nobody can see failing is a guest who never heard from the campsite.
const MaxAttempts = 8

// ErrNoHandler is returned when a message names a kind nothing is registered
// for. It is a deployment error — a handler removed while its messages were
// still queued — and is never treated as delivery.
var ErrNoHandler = errors.New("outbox: no handler for kind")

// Message is one owed side effect.
type Message struct {
	Kind    string
	Payload any
	// IdempotencyKey makes the enqueue itself idempotent, so a retried
	// transaction cannot owe the same email twice. Unique per (tenant, kind).
	IdempotencyKey string
}

// Enqueue writes a message on the caller's transaction.
//
// A duplicate key is not an error: it means this exact side effect is already
// owed, which is the outcome the caller wanted. That is the one place here
// where a collision is silently accepted, and it is safe precisely because the
// key names the effect rather than the attempt.
func Enqueue(ctx context.Context, q db.TX, m Message) error {
	if m.Kind == "" || m.IdempotencyKey == "" {
		return fmt.Errorf("outbox: kind and idempotency key are required")
	}

	payload, err := json.Marshal(m.Payload)
	if err != nil {
		return fmt.Errorf("encode payload: %w", err)
	}

	_, err = q.Exec(ctx, `
		insert into outbox_message (kind, payload, idempotency_key)
		values ($1, $2, $3)
		on conflict (tenant_id, kind, idempotency_key) do nothing`,
		m.Kind, payload, m.IdempotencyKey)
	if err != nil {
		return fmt.Errorf("enqueue %s: %w", m.Kind, err)
	}
	return nil
}

// Pending is a message handed to a handler.
type Pending struct {
	ID       string
	Kind     string
	Payload  []byte
	Attempts int
}

// Handler performs the side effect. It runs inside the dispatcher's
// transaction, so anything it writes to prove the effect happened commits or
// rolls back with the message being marked delivered.
type Handler func(ctx context.Context, q db.TX, m Pending) error

// backoff is when a failed message becomes available again. Linear rather than
// exponential: the failures this sees are a mail server being restarted or a
// template not existing yet, both of which are fixed in minutes, and an
// exponential curve would push the eighth attempt days out.
func backoff(attempts int) time.Duration {
	return time.Duration(attempts) * time.Minute
}

func errorsIsNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
