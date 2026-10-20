// Package state defines payout item statuses, legal transitions and invariants.
package state

import "strings"

type Status string

const (
	Created   Status = "CREATED"
	Pending   Status = "PENDING"
	Success   Status = "SUCCESS"
	Unclaimed Status = "UNCLAIMED"
	Failed    Status = "FAILED"
	OnHold    Status = "ONHOLD"
	Blocked   Status = "BLOCKED"
	Returned  Status = "RETURNED"
	Refunded  Status = "REFUNDED"
	Reversed  Status = "REVERSED"
	Canceled  Status = "CANCELED"
	Unknown   Status = "UNKNOWN"
)

// Modes label where an item's state came from. They are never mixed silently.
const (
	ModeLive = "LIVE_SANDBOX"
	ModeSim  = "SIMULATED"
	ModeFake = "FAKE"
)

// Parse normalises a PayPal transaction_status into a Status.
func Parse(s string) Status {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "CREATED":
		return Created
	case "PENDING", "PROCESSING":
		return Pending
	case "SUCCESS", "SUCCEEDED":
		return Success
	case "UNCLAIMED":
		return Unclaimed
	case "FAILED", "DENIED":
		return Failed
	case "ONHOLD", "ON_HOLD":
		return OnHold
	case "BLOCKED":
		return Blocked
	case "RETURNED":
		return Returned
	case "REFUNDED":
		return Refunded
	case "REVERSED":
		return Reversed
	case "CANCELED", "CANCELLED":
		return Canceled
	default:
		return Unknown
	}
}

// Live means the item still represents money that is, or may become, paid.
// At most one live item may exist per logical payout (enforced in code and by a
// partial unique index in the database).
func (s Status) Live() bool {
	switch s {
	case Created, Pending, Unclaimed, OnHold, Blocked, Success, Unknown:
		return true
	}
	return false
}

// NeedsPolling is true while PayPal may still change the item.
func (s Status) NeedsPolling() bool {
	switch s {
	case Created, Pending, Unclaimed, OnHold, Blocked, Unknown:
		return true
	}
	return false
}

// ReissueSafe is true only when PayPal has confirmed the money did not and will
// not reach the recipient from this item.
func (s Status) ReissueSafe() bool {
	return s == Failed || s == Returned || s == Canceled
}

var transitions = map[Status][]Status{
	Created:   {Pending, Success, Failed, Unclaimed, OnHold, Blocked},
	Pending:   {Success, Failed, Unclaimed, OnHold, Blocked, Returned},
	Unclaimed: {Success, Returned, Canceled},
	OnHold:    {Pending, Success, Failed, Blocked, Returned, Unclaimed},
	Blocked:   {Pending, Failed, Success, Returned},
	Success:   {Refunded, Reversed},
	Unknown:   {Created, Pending, Success, Failed, Unclaimed, OnHold, Blocked, Returned, Canceled},
}

// CanTransition reports whether moving from one status to another is legal.
// Staying in the same status is always allowed (idempotent).
func CanTransition(from, to Status) bool {
	if from == to {
		return true
	}
	for _, t := range transitions[from] {
		if t == to {
			return true
		}
	}
	return false
}
