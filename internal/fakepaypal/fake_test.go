package fakepaypal

import (
	"context"
	"testing"

	"github.com/mandar-1dev/payoutrescue/internal/paypal"
)

func TestOutcomesAndIdempotency(t *testing.T) {
	f := New()
	f.Register("ok@x.co")
	ctx := context.Background()
	req := paypal.CreateBatchRequest{SenderBatchID: "b1", Items: []paypal.CreateItem{
		{SenderItemID: "pr-aa-a1", Receiver: "ok@x.co", Currency: "USD", Value: "5.00"},
		{SenderItemID: "pr-bb-a1", Receiver: "nobody@x.co", Currency: "USD", Value: "5.00"},
		{SenderItemID: "pr-cc-a1", Receiver: "not-an-email", Currency: "USD", Value: "5.00"},
	}}
	r1, err := f.CreateBatch(ctx, req, "req1")
	if err != nil {
		t.Fatal(err)
	}
	r2, _ := f.CreateBatch(ctx, req, "req1")
	if r1.BatchID != r2.BatchID {
		t.Fatal("same request id must replay the same batch")
	}
	if _, err := f.CreateBatch(ctx, req, "req-other"); err == nil {
		t.Fatal("reused sender_batch_id with a new request id must be rejected")
	}
	b, _ := f.GetBatch(ctx, r1.BatchID)
	got := []string{b.Items[0].TransactionStatus, b.Items[1].TransactionStatus, b.Items[2].TransactionStatus}
	want := []string{"SUCCESS", "UNCLAIMED", "FAILED"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("statuses %v want %v", got, want)
		}
	}
	if len(f.Payments()) != 1 {
		t.Fatalf("only the registered receiver is paid: %v", f.Payments())
	}
}

func TestCancelClaimAndReturn(t *testing.T) {
	f := New()
	ctx := context.Background()
	mk := func(sb, sid string) string {
		r, _ := f.CreateBatch(ctx, paypal.CreateBatchRequest{SenderBatchID: sb, Items: []paypal.CreateItem{{SenderItemID: sid, Receiver: "u@x.co", Currency: "USD", Value: "1.00"}}}, sb)
		b, _ := f.GetBatch(ctx, r.BatchID)
		return b.Items[0].PayoutItemID
	}
	a := mk("b1", "pr-aa-a1")
	if _, err := f.CancelItem(ctx, a); err != nil {
		t.Fatal(err)
	}
	if f.Claim(a) {
		t.Fatal("claim after cancel must fail")
	}
	if _, err := f.CancelItem(ctx, a); err == nil {
		t.Fatal("second cancel must error")
	}
	b := mk("b2", "pr-bb-a1")
	f.Advance(30)
	it, _ := f.GetItem(ctx, b)
	if it.TransactionStatus != "RETURNED" {
		t.Fatalf("expected RETURNED after 30 days, got %s", it.TransactionStatus)
	}
}

func TestDuplicatesDetected(t *testing.T) {
	f := New()
	f.Register("u@x.co")
	ctx := context.Background()
	for i, sid := range []string{"naive-zz-1", "naive-zz-2"} {
		f.CreateBatch(ctx, paypal.CreateBatchRequest{SenderBatchID: sid, Items: []paypal.CreateItem{{SenderItemID: sid, Receiver: "u@x.co", Currency: "USD", Value: "1.00"}}}, string(rune('a'+i)))
	}
	if d := f.Duplicates(); len(d) != 1 || d[0] != "zz" {
		t.Fatalf("duplicates=%v", d)
	}
}
