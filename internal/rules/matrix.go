package rules

import (
	"fmt"
	"strings"

	"github.com/mandar-1dev/payoutrescue/internal/state"
)

// MatrixRow is one situation and what Decide proposes for it. The Help page renders
// this table from the real rules, so it can never drift from the code.
type MatrixRow struct {
	When      string     `json:"when"`
	Status    string     `json:"status"`
	ErrorCode string     `json:"error_code"`
	AgeDays   int        `json:"age_days"`
	Corrected bool       `json:"corrected_email"`
	Proposals []Proposal `json:"proposals"`
}

// KnownErrorCodes are the PayPal error names the rules understand (plus one unknown name).
var KnownErrorCodes = []string{
	"INVALID_EMAIL", "RECEIVER_UNREGISTERED", "RECEIVER_UNCONFIRMED", "RECEIVER_ACCOUNT_LOCKED",
	"RECEIVER_ACCOUNT_LIMITATION", "UNVERIFIED_RECIPIENT_NOT_SUPPORTED", "CURRENCY_NOT_SUPPORTED_FOR_RECEIVER",
	"REFUSED_RECEIVER_REFUSED", "RECEIVING_LIMIT_EXCEEDED", "RECEIVER_COUNTRY_NOT_ALLOWED", "REGULATORY_BLOCKED",
	"RISK_DECLINE", "DUPLICATE_ITEM", "SOMETHING_NEW_FROM_PAYPAL",
}

// Matrix runs Decide over every situation worth documenting.
func Matrix() []MatrixRow {
	var rows []MatrixRow
	add := func(when string, f Facts) {
		rows = append(rows, MatrixRow{When: when, Status: string(f.Status), ErrorCode: f.ErrorCode, AgeDays: f.AgeDays, Corrected: f.CorrectedEmail != "", Proposals: Decide(f)})
	}
	const email = "corrected@example.com"
	for _, age := range []int{0, 20, 27} {
		add(fmt.Sprintf("Unclaimed, day %d", age), Facts{Status: state.Unclaimed, AgeDays: age})
	}
	add("Unclaimed, with a corrected email", Facts{Status: state.Unclaimed, AgeDays: 5, CorrectedEmail: email})
	for _, code := range KnownErrorCodes {
		label := strings.ToLower(strings.ReplaceAll(code, "_", " "))
		add("Failed: "+label, Facts{Status: state.Failed, ErrorCode: code, Attempt: 1})
		add("Failed: "+label+", with a corrected email", Facts{Status: state.Failed, ErrorCode: code, Attempt: 1, CorrectedEmail: email})
	}
	add("Returned, no corrected email", Facts{Status: state.Returned, Attempt: 1})
	add("Returned, with a corrected email", Facts{Status: state.Returned, Attempt: 1, CorrectedEmail: email})
	add("Cancelled, with a corrected email", Facts{Status: state.Canceled, Attempt: 1, CorrectedEmail: email})
	add("Cancelled, with a corrected email, attempt limit reached", Facts{Status: state.Canceled, Attempt: MaxAttempts, CorrectedEmail: email})
	add("On hold", Facts{Status: state.OnHold})
	add("On hold, with a corrected email", Facts{Status: state.OnHold, CorrectedEmail: email})
	add("Blocked", Facts{Status: state.Blocked})
	add("Unknown status", Facts{Status: state.Unknown})
	add("Pending", Facts{Status: state.Pending})
	add("Paid", Facts{Status: state.Success})
	return rows
}
