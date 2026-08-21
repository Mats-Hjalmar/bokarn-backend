// Package logging hands out subsystem loggers that survive package
// initialisation order.
//
// The house pattern is a package-level `var logger = slog.With("subsystem",
// "x")`. That captures whatever handler is default at the moment the variable
// is initialised — and internal/otel installs the real handler from an init
// function, so a package that does not transitively import it captures Go's
// built-in handler instead. The built-in one routes through the log package,
// which slog.SetDefault has since redirected back into the new handler, so the
// record arrives as a whole formatted text line jammed into the msg field:
// structured on the outside, unreadable on the inside, and correct-looking
// enough that nobody notices until they try to filter on a field.
//
// New resolves the handler when a record is written rather than when the
// variable is initialised, which removes the ordering question instead of
// documenting it.
package logging

import (
	"context"
	"log/slog"
)

// New returns a logger tagged with its subsystem.
func New(subsystem string) *slog.Logger {
	return slog.New(deferred{
		attrs: []slog.Attr{slog.String("subsystem", subsystem)},
	})
}

// deferred forwards to whatever handler is default at write time, carrying its
// own attrs and groups because the handler it will eventually delegate to does
// not exist yet when they are added.
type deferred struct {
	attrs  []slog.Attr
	groups []string
}

func (d deferred) Enabled(ctx context.Context, level slog.Level) bool {
	return slog.Default().Handler().Enabled(ctx, level)
}

func (d deferred) Handle(ctx context.Context, r slog.Record) error {
	h := slog.Default().Handler()
	for _, g := range d.groups {
		h = h.WithGroup(g)
	}
	if len(d.attrs) > 0 {
		h = h.WithAttrs(d.attrs)
	}
	return h.Handle(ctx, r)
}

func (d deferred) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return d
	}
	return deferred{
		attrs:  append(append([]slog.Attr(nil), d.attrs...), attrs...),
		groups: d.groups,
	}
}

func (d deferred) WithGroup(name string) slog.Handler {
	if name == "" {
		return d
	}
	return deferred{
		attrs:  d.attrs,
		groups: append(append([]string(nil), d.groups...), name),
	}
}
