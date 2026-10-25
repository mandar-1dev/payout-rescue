package rules

import (
	"testing"

	"github.com/mandar-1dev/payoutrescue/internal/state"
)

func kinds(ps []Proposal) map[Kind]bool {
	m := map[Kind]bool{}
	for _, p := range ps {
		m[p.Kind] = true
	}
	return m
}

func TestNormalize(t *testing.T) {
	if NormalizeError(" invalid_email_id ") != "INVALID_EMAIL" {
		t.Fatal("INVALID_EMAIL_ID must normalise")
	}
}

func TestInvalidEmailFlow(t *testing.T) {
	ps := Decide(Facts{Status: state.Failed, ErrorCode: "INVALID_EMAIL"})
	k := kinds(ps)
	if !k[KindNotify] || k[KindReissue] {
		t.Fatalf("before correction expect notify only: %+v", ps)
	}
	ps = Decide(Facts{Status: state.Failed, ErrorCode: "INVALID_EMAIL_ID", CorrectedEmail: "a@b.co"})
	if !kinds(ps)[KindReissue] {
		t.Fatalf("after correction expect reissue: %+v", ps)
	}
}

func TestNeverReissue(t *testing.T) {
	for _, code := range []string{"RECEIVER_COUNTRY_NOT_ALLOWED", "REGULATORY_BLOCKED", "RISK_DECLINE", "CURRENCY_NOT_SUPPORTED_FOR_RECEIVER", "DUPLICATE_ITEM", "WHATEVER"} {
		ps := Decide(Facts{Status: state.Failed, ErrorCode: code, CorrectedEmail: "x@y.co"})
		if kinds(ps)[KindReissue] {
			t.Errorf("%s must never be re-issued", code)
		}
	}
	for _, st := range []state.Status{state.OnHold, state.Blocked, state.Pending, state.Success} {
		if kinds(Decide(Facts{Status: st, CorrectedEmail: "x@y.co"}))[KindReissue] {
			t.Errorf("%s must never be re-issued", st)
		}
	}
}

func TestUnclaimedLifecycle(t *testing.T) {
	cases := []struct {
		age  int
		tpl  string
		canc bool
	}{{0, TplUnclaimedSignup, false}, {19, TplUnclaimedSignup, false}, {20, TplReminderD20, false}, {26, TplReminderD20, false}, {27, TplFinalD27, true}, {29, TplFinalD27, true}}
	for _, c := range cases {
		ps := Decide(Facts{Status: state.Unclaimed, AgeDays: c.age})
		if ps[0].TemplateKey != c.tpl {
			t.Errorf("age %d: template %s want %s", c.age, ps[0].TemplateKey, c.tpl)
		}
		if kinds(ps)[KindCancel] != c.canc {
			t.Errorf("age %d: cancel=%v want %v", c.age, kinds(ps)[KindCancel], c.canc)
		}
	}
	ps := Decide(Facts{Status: state.Unclaimed, AgeDays: 5, CorrectedEmail: "n@e.co"})
	if len(ps) != 1 || ps[0].Kind != KindCancel {
		t.Fatalf("corrected email on unclaimed must cancel first: %+v", ps)
	}
}

func TestCanceledReissueAndMaxAttempts(t *testing.T) {
	if !kinds(Decide(Facts{Status: state.Canceled, CorrectedEmail: "n@e.co", Attempt: 1}))[KindReissue] {
		t.Fatal("canceled + corrected email should propose reissue")
	}
	if kinds(Decide(Facts{Status: state.Canceled, CorrectedEmail: "n@e.co", Attempt: MaxAttempts}))[KindReissue] {
		t.Fatal("max attempts reached")
	}
}

func TestMoneyMoving(t *testing.T) {
	if !KindCancel.MoneyMoving() || !KindReissue.MoneyMoving() || KindNotify.MoneyMoving() {
		t.Fatal("money moving kinds wrong")
	}
}

func TestMatrixIsGeneratedFromDecide(t *testing.T) {
	rows := Matrix()
	if len(rows) < 30 {
		t.Fatalf("expected a full matrix, got %d rows", len(rows))
	}
	for _, r := range rows {
		if r.When == "" {
			t.Fatal("every row needs a description")
		}
		for _, p := range r.Proposals {
			if p.Kind == KindReissue && !r.Corrected {
				t.Errorf("%s: a replacement must never be proposed without a corrected email", r.When)
			}
			if p.Kind == KindReissue && (r.Status == "ONHOLD" || r.Status == "BLOCKED" || r.Status == "UNCLAIMED" || r.Status == "PENDING") {
				t.Errorf("%s: a replacement must never follow a live item", r.When)
			}
		}
	}
}
