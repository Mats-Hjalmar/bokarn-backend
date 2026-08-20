// Package audit records who changed what, and why.
//
// An entry is written by the handler inside the same transaction as the change
// it describes, so an audited mutation cannot commit without its trail and a
// rolled-back one leaves none. The mutations that exist today — maintenance
// blocks and season repricing — are not yet audited; this is the mechanism the
// domains that handle money and guest data will use.
package audit

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Mats-Hjalmar/bokarn-backend/internal/db"
)

// Entry is one recorded change. Changes is a per-field diff; a mutation that
// altered nothing writes no entry at all rather than an empty one.
type Entry struct {
	ActorID    *string
	Action     string
	EntityType string
	EntityID   string
	Changes    map[string]Change
	Reason     string
}

// Change is one field's before and after value.
type Change struct {
	Old any `json:"old"`
	New any `json:"new"`
}

// Write appends an entry on the caller's transaction.
//
// This is not a database trigger on purpose: a trigger sees the row but not the
// actor, the request, or the reason, and reconstructing those inside Postgres
// would mean smuggling them through session state.
func Write(ctx context.Context, q db.TX, e Entry) error {
	if e.Action == "" || e.EntityType == "" {
		return fmt.Errorf("audit: action and entity type are required")
	}

	var changes []byte
	if len(e.Changes) > 0 {
		encoded, err := json.Marshal(e.Changes)
		if err != nil {
			return fmt.Errorf("encode changes: %w", err)
		}
		changes = encoded
	}

	_, err := q.Exec(ctx,
		`insert into audit_log
		     (actor_id, action, entity_type, entity_id, changes, reason)
		 values ($1, $2, $3, $4, $5, $6)`,
		e.ActorID, e.Action, e.EntityType, e.EntityID, changes, e.Reason,
	)
	if err != nil {
		return fmt.Errorf("insert audit entry: %w", err)
	}
	return nil
}
