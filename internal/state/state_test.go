package state

import "testing"

func TestParse(t *testing.T) {
	cases := map[string]Status{"success": Success, "UNCLAIMED": Unclaimed, "Cancelled": Canceled, "processing": Pending, "denied": Failed, "weird": Unknown}
	for in, want := range cases {
		if got := Parse(in); got != want {
			t.Errorf("Parse(%q)=%s want %s", in, got, want)
		}
	}
}

func TestLiveAndReissueSafe(t *testing.T) {
	for _, s := range []Status{Created, Pending, Unclaimed, OnHold, Blocked, Success} {
		if !s.Live() {
			t.Errorf("%s should be live", s)
		}
		if s.ReissueSafe() {
			t.Errorf("%s must never be reissue-safe", s)
		}
	}
	for _, s := range []Status{Failed, Returned, Canceled} {
		if s.Live() || !s.ReissueSafe() {
			t.Errorf("%s should be dead and reissue-safe", s)
		}
	}
	for _, s := range []Status{Refunded, Reversed} {
		if s.ReissueSafe() {
			t.Errorf("%s must not auto-reissue", s)
		}
	}
}

func TestTransitions(t *testing.T) {
	if !CanTransition(Unclaimed, Returned) || !CanTransition(Unclaimed, Canceled) || !CanTransition(Unclaimed, Success) {
		t.Fatal("unclaimed transitions missing")
	}
	for _, term := range []Status{Failed, Returned, Canceled, Refunded, Reversed} {
		for _, to := range []Status{Pending, Success, Unclaimed, OnHold} {
			if CanTransition(term, to) {
				t.Errorf("terminal %s must not move to %s", term, to)
			}
		}
	}
	if !CanTransition(Success, Success) {
		t.Fatal("same-status transition must be a no-op allowed")
	}
	if CanTransition(Success, Unclaimed) {
		t.Fatal("success must not go back to unclaimed")
	}
}
