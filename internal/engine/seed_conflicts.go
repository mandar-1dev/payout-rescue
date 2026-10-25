package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/mandar-1dev/payoutrescue/internal/demo"
	"github.com/mandar-1dev/payoutrescue/internal/state"
	"github.com/mandar-1dev/payoutrescue/internal/store"
)

var ErrAlreadySeeded = errors.New("conflict demo pack already seeded")

// SeedConflicts creates the 7 FAKE conflict payouts. Every situation is produced through the
// real flows (replies, readbacks, approvals), never by writing statuses directly, so the
// audit log and the state machine stay honest.
func (e *Engine) SeedConflicts(ctx context.Context) error {
	if e.Fake == nil {
		return errors.New("fake PayPal is disabled; the conflict pack needs it")
	}
	var count int
	if err := e.Store.DB.QueryRowContext(ctx, `SELECT count(*) FROM logical_payouts WHERE purpose = 'Conflict demo'`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return ErrAlreadySeeded
	}

	var recs []Recipient
	for _, c := range demo.Conflicts() {
		recs = append(recs, Recipient{Name: c.Name, Email: c.Email, Amount: c.Amount, Currency: c.Currency, Purpose: c.Purpose, FakeOutcome: c.FakeOutcome})
	}
	_, created, err := e.CreateBatch(ctx, CreateBatchInput{Mode: state.ModeFake, Recipients: recs})
	if err != nil && !errors.Is(err, ErrWillRetry) {
		return err
	}
	// CreateBatch returns the rows as inserted; reload them so PayPal ids and statuses are current.
	fresh := func(name string) (*store.Item, error) {
		for _, c := range created {
			it, err := store.GetItem(ctx, e.Store.DB, c.ID, false)
			if err != nil {
				return nil, err
			}
			if it.RecipientName == name {
				return it, nil
			}
		}
		return nil, fmt.Errorf("seed: payout %q not found", name)
	}

	// 1. Priya: a cancel is proposed, then the recipient claims before it is approved -> claim_race
	it, err := fresh("Priya Nair")
	if err != nil {
		return err
	}
	if err := e.SetCorrectedEmail(ctx, it.ID, "priya.alternate@example.com", "seed"); err != nil {
		return err
	}
	e.Fake.Claim(it.PayPalItemID)
	if err := e.ProcessItem(ctx, it.ID); err != nil {
		return err
	}

	// 2. Omar: a reply names two emails -> ambiguous_reply
	if it, err = fresh("Omar Haddad"); err != nil {
		return err
	}
	if _, _, err := e.InboundReply(ctx, it.ID, "Please use omar.new@example.com or omar.work@example.com"); err != nil {
		return err
	}

	// 3. Sofia: the reply repeats the email already on file -> email_same_as_on_file
	if it, err = fresh("Sofia Rossi"); err != nil {
		return err
	}
	if _, _, err := e.InboundReply(ctx, it.ID, "please use sofia.rossi@example.com"); err != nil {
		return err
	}

	// 4. Tariq: declines while a cancel is proposed -> refusal_vs_proposal
	if it, err = fresh("Tariq Aziz"); err != nil {
		return err
	}
	if err := e.SetCorrectedEmail(ctx, it.ID, "tariq.temp@example.com", "seed"); err != nil {
		return err
	}
	if _, _, err := e.InboundReply(ctx, it.ID, "no thanks, I do not want it"); err != nil {
		return err
	}

	// 5. Mei: on hold, then a corrected email arrives -> held_with_correction
	if it, err = fresh("Mei Lin"); err != nil {
		return err
	}
	if err := e.SetCorrectedEmail(ctx, it.ID, "mei.lin.new@example.com", "seed"); err != nil {
		return err
	}

	// 6. Noah: two approved replacements each fail, so the third attempt is the last -> attempts_exhausted
	if it, err = fresh("Noah Brandt"); err != nil {
		return err
	}
	logicalID := it.LogicalPayoutID
	for i := 1; i <= 3; i++ {
		e.Fake.SetOutcome(fmt.Sprintf("noah.fix%d@example.com", i), "FAILED", "INVALID_EMAIL")
	}
	for i := 1; i <= 3; i++ {
		var id string
		if err := e.Store.DB.QueryRowContext(ctx, `SELECT id FROM payout_items WHERE logical_payout_id=$1 ORDER BY attempt DESC LIMIT 1`, logicalID).Scan(&id); err != nil {
			return err
		}
		if err := e.SetCorrectedEmail(ctx, id, fmt.Sprintf("noah.fix%d@example.com", i), "seed"); err != nil {
			return err
		}
		if i == 3 {
			break // the third correction finds no attempts left and opens the conflict
		}
		as, err := store.ListActions(ctx, e.Store.DB, "proposed", id, 50)
		if err != nil {
			return err
		}
		approved := false
		for _, a := range as {
			if a.Kind == "reissue" {
				if _, err := e.Approve(ctx, a.ID, "seed"); err != nil && !errors.Is(err, ErrWillRetry) {
					return err
				}
				approved = true
			}
		}
		if !approved {
			return errors.New("seed: expected a replacement proposal for Noah")
		}
	}

	// 7. Zoe: claimed, then PayPal "un-claims" it, which is impossible -> status_disagreement
	if it, err = fresh("Zoe Park"); err != nil {
		return err
	}
	e.Fake.Claim(it.PayPalItemID)
	if err := e.ProcessItem(ctx, it.ID); err != nil {
		return err
	}
	e.Fake.SetStatus(it.PayPalItemID, "UNCLAIMED", "RECEIVER_UNREGISTERED")
	return e.ProcessItem(ctx, it.ID)
}

var _ = sql.ErrNoRows
