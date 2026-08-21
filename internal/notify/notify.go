// Package notify writes to guests.
//
// A template belongs to the operator, in the guest's language, and there is no
// built-in default: a locale with no template is an error at send time, not a
// message in the wrong language and not silence. The campsite's confirmation
// email is the campsite's voice.
//
// Sending is idempotent by construction rather than by care. Every send writes
// a message_log row keyed on the outbox message that caused it, in the same
// transaction, so the second delivery attempt after a crash collides on a
// unique index instead of mailing the guest twice.
package notify

import (
	"context"
	"errors"
	"fmt"
)

// Template keys. Each is a row per locale in message_template.
const (
	KeyBookingConfirmed = "booking_confirmed"
)

// ErrNoTemplate is returned when the operator has no template for this key and
// locale. It is a configuration gap, and it fails the delivery so the outbox
// retries once the template exists.
var ErrNoTemplate = errors.New("notify: no template for key and locale")

// ErrAlreadySent is returned when this outbox message has already produced a
// message. The dispatcher treats it as success: the effect it was asked for has
// happened, which is the only thing at-least-once delivery can promise.
var ErrAlreadySent = errors.New("notify: already sent")

// Template is one operator's wording for one key in one language.
type Template struct {
	Key     string
	Locale  string
	Subject string
	Body    string
}

// MissingFieldError names a placeholder the template used and the caller did
// not supply. Rendering stops rather than emitting an empty string, because a
// confirmation email that says "Din bokning  är bekräftad" is worse than one
// that has not been sent yet.
type MissingFieldError struct {
	Key   string
	Field string
}

func (e MissingFieldError) Error() string {
	return fmt.Sprintf(
		"notify: template %q uses %q, which was not supplied", e.Key, e.Field)
}

// Message is a rendered message ready for a transport.
type Message struct {
	To      string
	Subject string
	Body    string
}

// Transport delivers a rendered message. It is an interface so the e2e suite
// can assert on what would have been sent without standing up a mail server.
type Transport interface {
	Send(ctx context.Context, m Message) error
}
