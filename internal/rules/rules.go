// Package rules maps an item's facts to proposed actions. It is pure and has
// no side effects, so it is easy to test and to change.
package rules

import (
	"strings"

	"github.com/mandar-1dev/payoutrescue/internal/state"
)

type Kind string

const (
	KindNotify   Kind = "notify"   // draft a recipient message
	KindCancel   Kind = "cancel"   // cancel an UNCLAIMED item (money-moving)
	KindReissue  Kind = "reissue"  // create exactly one replacement (money-moving)
	KindEscalate Kind = "escalate" // needs a human decision, never automated
	KindFlag     Kind = "flag"     // informational
)

// MoneyMoving kinds always require a recorded human approval.
func (k Kind) MoneyMoving() bool { return k == KindCancel || k == KindReissue }

// Template keys (see internal/ai/templates.go).
const (
	TplUnclaimedSignup = "UNCLAIMED_SIGNUP"
	TplReminderD20     = "UNCLAIMED_REMINDER_D20"
	TplFinalD27        = "UNCLAIMED_FINAL_D27"
	TplInvalidEmail    = "INVALID_EMAIL"
	TplAccountIssue    = "ACCOUNT_ISSUE"
	TplCurrency        = "CURRENCY_NOT_SUPPORTED"
	TplLimit           = "LIMIT_EXCEEDED"
	TplCountry         = "COUNTRY_NOT_ALLOWED"
	TplReissueConfirm  = "REISSUE_CONFIRMATION"
	TplCancelledNotice = "CANCELLED_NOTICE"
)

const MaxAttempts = 3

// Facts is everything the rules may look at.
type Facts struct {
	Status         state.Status
	ErrorCode      string
	AgeDays        int
	Attempt        int
	CorrectedEmail string // already validated in code
	NeedsReview    bool
}

type Proposal struct {
	Kind        Kind
	TemplateKey string
	Reason      string
}

// NormalizeError maps PayPal error names to the canonical code used by the rules.
// PayPal's error table says INVALID_EMAIL_ID while its simulator says INVALID_EMAIL.
func NormalizeError(code string) string {
	c := strings.ToUpper(strings.TrimSpace(code))
	if c == "INVALID_EMAIL_ID" {
		return "INVALID_EMAIL"
	}
	return c
}

var reissueAfterCorrection = map[string]bool{
	"INVALID_EMAIL":                      true,
	"RECEIVER_UNREGISTERED":              true,
	"RECEIVER_UNCONFIRMED":               true,
	"RECEIVER_ACCOUNT_LOCKED":            true,
	"RECEIVER_ACCOUNT_LIMITATION":        true,
	"UNVERIFIED_RECIPIENT_NOT_SUPPORTED": true,
}

// Decide returns the proposals for an item. Duplicates are removed by the
// engine through idempotency keys, so Decide may be called repeatedly.
func Decide(f Facts) []Proposal {
	code := NormalizeError(f.ErrorCode)
	var out []Proposal
	add := func(k Kind, tpl, why string) { out = append(out, Proposal{k, tpl, why}) }

	switch f.Status {
	case state.Unclaimed:
		if f.CorrectedEmail != "" {
			add(KindCancel, "", "Recipient supplied a corrected email: cancel the unclaimed item first, then replace it once")
			return out
		}
		switch {
		case f.AgeDays >= 27:
			add(KindNotify, TplFinalD27, "Day 27: PayPal returns unclaimed money at day 30")
			add(KindCancel, "", "Cancel before the automatic return so the outcome is confirmed, not assumed")
		case f.AgeDays >= 20:
			add(KindNotify, TplReminderD20, "Day 20 reminder")
		default:
			add(KindNotify, TplUnclaimedSignup, "Recipient has no confirmed PayPal account")
		}
	case state.Failed:
		out = append(out, decideFailed(code, f)...)
	case state.Returned, state.Canceled:
		switch {
		case f.CorrectedEmail != "" && f.Attempt < MaxAttempts:
			add(KindReissue, TplReissueConfirm, "Original is confirmed "+string(f.Status)+" and a corrected email exists")
		default:
			add(KindNotify, TplCancelledNotice, "Payout was "+strings.ToLower(string(f.Status)))
		}
	case state.OnHold:
		add(KindFlag, "", "On hold at PayPal: wait; never re-issue")
	case state.Blocked:
		add(KindEscalate, "", "Blocked at PayPal: needs a human review; never re-issue")
	case state.Unknown:
		add(KindEscalate, "", "PayPal returned a status this system does not recognise")
	}
	if f.NeedsReview {
		add(KindEscalate, "", "Item flagged for manual review")
	}
	return out
}

func decideFailed(code string, f Facts) []Proposal {
	var out []Proposal
	add := func(k Kind, tpl, why string) { out = append(out, Proposal{k, tpl, why}) }
	canReissue := reissueAfterCorrection[code] && f.CorrectedEmail != "" && f.Attempt < MaxAttempts
	switch code {
	case "INVALID_EMAIL":
		add(KindNotify, TplInvalidEmail, "The email on file is not valid: ask for the PayPal email")
	case "RECEIVER_UNREGISTERED", "RECEIVER_UNCONFIRMED":
		add(KindNotify, TplUnclaimedSignup, "Recipient has no confirmed PayPal account")
	case "RECEIVER_ACCOUNT_LOCKED", "RECEIVER_ACCOUNT_LIMITATION", "UNVERIFIED_RECIPIENT_NOT_SUPPORTED":
		add(KindNotify, TplAccountIssue, "Receiving account has a restriction; ask for an alternate account")
	case "CURRENCY_NOT_SUPPORTED_FOR_RECEIVER", "REFUSED_RECEIVER_REFUSED":
		add(KindNotify, TplCurrency, "Recipient cannot receive this currency")
		add(KindEscalate, "", "Changing currency needs an operator-entered amount; it is never taken from AI output")
	case "RECEIVING_LIMIT_EXCEEDED":
		add(KindNotify, TplLimit, "Recipient has reached a receiving limit; wait for them to resolve it")
	case "RECEIVER_COUNTRY_NOT_ALLOWED", "REGULATORY_BLOCKED", "RISK_DECLINE":
		add(KindNotify, TplCountry, "PayPal will not allow this payout")
		add(KindEscalate, "", "Compliance or risk decline: never re-issue")
	case "DUPLICATE_ITEM":
		add(KindFlag, "", "PayPal reports a duplicate item: no action")
	default:
		add(KindEscalate, "", "Unrecognised error code "+code+": needs a human")
	}
	if canReissue {
		add(KindReissue, TplReissueConfirm, "Original is confirmed FAILED and a corrected email exists")
	}
	return out
}
