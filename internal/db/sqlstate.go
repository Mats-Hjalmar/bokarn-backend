package db

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// Postgres SQLSTATE codes the stores map to domain outcomes. Every code is a
// generic Postgres condition.
const (
	// InvalidTextRepresentation (22P02) — a value can't be parsed as its cast
	// type, e.g. an id path param that isn't a valid uuid. Stores map it to
	// their not-found errors.
	InvalidTextRepresentation = "22P02"
	// UniqueViolation (23505) — a duplicate key. Often mapped to "already
	// exists" or treated as idempotent success.
	UniqueViolation = "23505"
	// ForeignKeyViolation (23503) — a referenced row does not exist.
	ForeignKeyViolation = "23503"
	// CheckViolation (23514) — a table check constraint tripped.
	CheckViolation = "23514"
)

// Codes the booking domain depends on for correctness, not just for nicer
// error messages.
const (
	// NotNullViolation (23502) — a NOT NULL column got NULL. On a tenant table
	// this is the tenant guard firing: current_tenant_id() returned NULL
	// because no tenant was pinned, so the write fails closed.
	NotNullViolation = "23502"
	// ExclusionViolation (23P01) — an EXCLUDE constraint tripped. On
	// unit_allocation this is the double-booking guarantee: two stays overlap
	// on one unit. The assignment loop treats it as "try the next unit".
	ExclusionViolation = "23P01"
	// SerializationFailure (40001) — the transaction lost a serialization
	// race and is safe to retry as-is.
	SerializationFailure = "40001"
	// DeadlockDetected (40P01) — Postgres broke a lock cycle by aborting this
	// transaction. Retryable, but a recurrence means a lock-ordering bug.
	DeadlockDetected = "40P01"
	// InsufficientPrivilege (42501) — the role may not touch this row or
	// relation. Under RLS this is a cross-tenant write being refused.
	InsufficientPrivilege = "42501"
)

// Codes that mean the client sent something the database could not parse or
// store. They are client errors, and reporting them as 500s both misleads the
// caller and hides real faults in the error budget.
const (
	// NumericValueOutOfRange (22003) — a value exceeded its column's type, e.g.
	// a smallint given 99999.
	NumericValueOutOfRange = "22003"
	// StringDataRightTruncation (22001) — a value was longer than its column.
	StringDataRightTruncation = "22001"
)

// IsBadInput reports whether an error is the database refusing malformed input
// rather than a fault.
func IsBadInput(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	switch pgErr.Code {
	case InvalidTextRepresentation, NumericValueOutOfRange,
		StringDataRightTruncation, CheckViolation:
		return true
	}
	return false
}
