// Package fakepaypal is an in-memory stand-in for the Payouts API. It supports
// states PayPal's sandbox cannot produce on demand (ONHOLD, BLOCKED, the day-30
// return, a late claim) and fault injection. Everything it produces is labelled
// FAKE by the engine and the benchmark.
package fakepaypal

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/mandar-1dev/payoutrescue/internal/paypal"
)

type Outcome struct {
	Status string
	Err    string
}

type fitem struct {
	info       paypal.ItemInfo
	createdDay int
}

type Payment struct {
	ItemID, SenderItemID, Receiver, Value, Currency string
}

type Fake struct {
	mu         sync.Mutex
	seq        int
	day        int
	batches    map[string]*paypal.BatchResult
	order      map[string][]string
	items      map[string]*fitem
	bySender   map[string]string
	byRequest  map[string]string
	registered map[string]bool
	outcomes   map[string]Outcome
	payments   []Payment

	FailCreate   int  // return HTTP 500 without creating, this many times
	DropResponse int  // create the batch but return an error, this many times
	VerifyOK     bool // result of VerifyWebhook
	CreateCalls  int
}

func New() *Fake {
	return &Fake{
		batches: map[string]*paypal.BatchResult{}, order: map[string][]string{}, items: map[string]*fitem{},
		bySender: map[string]string{}, byRequest: map[string]string{},
		registered: map[string]bool{}, outcomes: map[string]Outcome{}, VerifyOK: true,
	}
}

var _ paypal.Client = (*Fake)(nil)

// Register marks an email as having a confirmed PayPal account.
func (f *Fake) Register(email string) {
	f.mu.Lock()
	f.registered[strings.ToLower(email)] = true
	f.mu.Unlock()
}

// SetOutcome scripts what a receiver's item will become on creation.
func (f *Fake) SetOutcome(email, status, errName string) {
	f.mu.Lock()
	f.outcomes[strings.ToLower(email)] = Outcome{status, errName}
	f.mu.Unlock()
}

func (f *Fake) decide(email string) Outcome {
	e := strings.ToLower(email)
	if o, ok := f.outcomes[e]; ok {
		return o
	}
	if !strings.Contains(e, "@") || strings.HasPrefix(e, "@") || strings.HasSuffix(e, "@") {
		return Outcome{"FAILED", "INVALID_EMAIL"}
	}
	if f.registered[e] {
		return Outcome{"SUCCESS", ""}
	}
	return Outcome{"UNCLAIMED", "RECEIVER_UNREGISTERED"}
}

func (f *Fake) snapshot(id string) paypal.BatchResult {
	b := *f.batches[id]
	b.Items = nil
	for _, iid := range f.order[id] {
		b.Items = append(b.Items, f.items[iid].info)
	}
	return b
}

func (f *Fake) pay(it *fitem) {
	f.payments = append(f.payments, Payment{it.info.PayoutItemID, it.info.SenderItemID, it.info.Receiver, it.info.Value, it.info.Currency})
}

func (f *Fake) CreateBatch(_ context.Context, r paypal.CreateBatchRequest, requestID string) (paypal.BatchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.CreateCalls++
	if f.FailCreate > 0 {
		f.FailCreate--
		return paypal.BatchResult{}, &paypal.APIError{Status: 500, Name: "INTERNAL_ERROR", Message: "injected"}
	}
	if id, ok := f.byRequest[requestID]; ok && requestID != "" {
		return f.snapshot(id), nil // idempotent replay
	}
	if _, ok := f.bySender[r.SenderBatchID]; ok {
		return paypal.BatchResult{}, &paypal.APIError{Status: 400, Name: "USER_BUSINESS_ERROR", Message: "duplicate sender_batch_id"}
	}
	f.seq++
	bid := fmt.Sprintf("FB%05d", f.seq)
	f.batches[bid] = &paypal.BatchResult{BatchID: bid, BatchStatus: "PENDING", SenderBatchID: r.SenderBatchID}
	f.bySender[r.SenderBatchID] = bid
	if requestID != "" {
		f.byRequest[requestID] = bid
	}
	for _, ci := range r.Items {
		f.seq++
		o := f.decide(ci.Receiver)
		it := &fitem{createdDay: f.day, info: paypal.ItemInfo{
			PayoutItemID: fmt.Sprintf("FI%05d", f.seq), PayoutBatchID: bid, SenderItemID: ci.SenderItemID,
			TransactionStatus: o.Status, Receiver: ci.Receiver, Currency: ci.Currency, Value: ci.Value, ErrorName: o.Err,
		}}
		if o.Status == "SUCCESS" {
			it.info.ErrorName = ""
			f.pay(it)
		}
		f.items[it.info.PayoutItemID] = it
		f.order[bid] = append(f.order[bid], it.info.PayoutItemID)
	}
	if f.DropResponse > 0 {
		f.DropResponse--
		return paypal.BatchResult{}, &paypal.APIError{Status: 504, Name: "REQUEST_TIMEOUT_EXCEEDED", Message: "response lost"}
	}
	return paypal.BatchResult{BatchID: bid, BatchStatus: "PENDING", SenderBatchID: r.SenderBatchID}, nil
}

func (f *Fake) GetBatch(_ context.Context, id string) (paypal.BatchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.batches[id]; !ok {
		return paypal.BatchResult{}, &paypal.APIError{Status: 404, Name: "INVALID_RESOURCE_ID", Message: "no such batch"}
	}
	return f.snapshot(id), nil
}

func (f *Fake) GetItem(_ context.Context, id string) (paypal.ItemInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.items[id]
	if !ok {
		return paypal.ItemInfo{}, &paypal.APIError{Status: 404, Name: "INVALID_RESOURCE_ID", Message: "no such item"}
	}
	return it.info, nil
}

func (f *Fake) CancelItem(_ context.Context, id string) (paypal.ItemInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.items[id]
	if !ok {
		return paypal.ItemInfo{}, &paypal.APIError{Status: 404, Name: "INVALID_RESOURCE_ID", Message: "no such item"}
	}
	switch it.info.TransactionStatus {
	case "UNCLAIMED":
		it.info.TransactionStatus, it.info.ErrorName = "CANCELED", ""
		return it.info, nil
	case "CANCELED":
		return paypal.ItemInfo{}, &paypal.APIError{Status: 422, Name: "ITEM_ALREADY_CANCELLED", Message: "already cancelled"}
	default:
		return paypal.ItemInfo{}, &paypal.APIError{Status: 422, Name: "ITEM_INCORRECT_STATUS", Message: "only unclaimed items can be cancelled"}
	}
}

func (f *Fake) VerifyWebhook(context.Context, http.Header, []byte, string) (bool, error) {
	return f.VerifyOK, nil
}

// Claim simulates the recipient claiming an unclaimed payout. It returns false
// when it is too late (the item was cancelled or returned).
func (f *Fake) Claim(itemID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.items[itemID]
	if !ok || it.info.TransactionStatus != "UNCLAIMED" {
		return false
	}
	it.info.TransactionStatus, it.info.ErrorName = "SUCCESS", ""
	f.pay(it)
	return true
}

// SetStatus scripts an arbitrary status (ONHOLD, BLOCKED, ...).
func (f *Fake) SetStatus(itemID, status, errName string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if it, ok := f.items[itemID]; ok {
		it.info.TransactionStatus, it.info.ErrorName = status, errName
		if status == "SUCCESS" {
			f.pay(it)
		}
	}
}

// Advance moves the fake clock; unclaimed items older than 30 days are returned.
func (f *Fake) Advance(days int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.day += days
	for _, it := range f.items {
		if it.info.TransactionStatus == "UNCLAIMED" && f.day-it.createdDay >= 30 {
			it.info.TransactionStatus = "RETURNED"
		}
	}
}

func (f *Fake) Payments() []Payment {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Payment(nil), f.payments...)
}

var logicalRe = regexp.MustCompile(`^[a-z]+-([a-z0-9]+)-`)

// LogicalKey extracts the logical payout key from a sender_item_id such as
// pr-1a2b3c4d-a1 or naive-1a2b3c4d-2.
func LogicalKey(senderItemID string) string {
	m := logicalRe.FindStringSubmatch(senderItemID)
	if m == nil {
		return senderItemID
	}
	return m[1]
}

// Duplicates returns logical payouts that were paid more than once.
func (f *Fake) Duplicates() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := map[string]int{}
	for _, p := range f.payments {
		count[LogicalKey(p.SenderItemID)]++
	}
	var out []string
	for k, n := range count {
		if n > 1 {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// ItemsByLogical returns every item whose sender_item_id belongs to the logical key,
// oldest first. It lets the benchmark model a recipient who claims everything PayPal
// emailed them, including items a client script lost track of.
func (f *Fake) ItemsByLogical(key string) []paypal.ItemInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []paypal.ItemInfo
	for _, it := range f.items {
		if LogicalKey(it.info.SenderItemID) == key {
			out = append(out, it.info)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PayoutItemID < out[j].PayoutItemID })
	return out
}

// ClaimAll claims every unclaimed item for a logical key and returns how many were claimed.
func (f *Fake) ClaimAll(key string) int {
	n := 0
	for _, it := range f.ItemsByLogical(key) {
		if it.TransactionStatus == "UNCLAIMED" && f.Claim(it.PayoutItemID) {
			n++
		}
	}
	return n
}

// RestoreItem re-creates an item after a process restart so FAKE items stored in
// the database can keep progressing. ageDays is how long ago the item was created.
func (f *Fake) RestoreItem(ppBatchID, senderBatchID string, it paypal.ItemInfo, ageDays int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	bump := func(id string) {
		var n int
		if _, err := fmt.Sscanf(id[2:], "%d", &n); err == nil && n > f.seq {
			f.seq = n
		}
	}
	if len(ppBatchID) > 2 {
		bump(ppBatchID)
	}
	if len(it.PayoutItemID) > 2 {
		bump(it.PayoutItemID)
	}
	if _, ok := f.batches[ppBatchID]; !ok {
		f.batches[ppBatchID] = &paypal.BatchResult{BatchID: ppBatchID, BatchStatus: "PENDING", SenderBatchID: senderBatchID}
		f.bySender[senderBatchID] = ppBatchID
	}
	it.PayoutBatchID = ppBatchID
	f.items[it.PayoutItemID] = &fitem{info: it, createdDay: f.day - ageDays}
	f.order[ppBatchID] = append(f.order[ppBatchID], it.PayoutItemID)
}
