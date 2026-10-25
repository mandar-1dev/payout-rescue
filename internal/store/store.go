// Package store is the PostgreSQL persistence layer.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lib/pq"
	"github.com/mandar-1dev/payoutrescue/internal/state"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// ErrLiveItemExists is returned when inserting a second live item for a logical
// payout. It is the database enforcing "never pay twice".
var ErrLiveItemExists = errors.New("a live payout item already exists for this logical payout")

var ErrNotFound = errors.New("not found")

type Store struct{ DB *sql.DB }

// Q is satisfied by *sql.DB and *sql.Tx.
type Q interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func Open(ctx context.Context, url string) (*Store, error) {
	db, err := sql.Open("postgres", url)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(12)
	db.SetConnMaxLifetime(30 * time.Minute)
	pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := db.PingContext(pctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("database ping: %w", err)
	}
	return &Store{DB: db}, nil
}

func (s *Store) Close() error { return s.DB.Close() }

// Tx runs fn in a transaction and commits only if fn returns nil.
func (s *Store) Tx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Migrate applies embedded SQL migrations once, guarded by an advisory lock.
func (s *Store) Migrate(ctx context.Context) error {
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock(7001)`); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(7001)`)
	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, n := range names {
		var done bool
		if err := conn.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name=$1)`, n).Scan(&done); err != nil {
			return err
		}
		if done {
			continue
		}
		body, _ := migrationFS.ReadFile("migrations/" + n)
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %s: %w", n, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(name) VALUES ($1)`, n); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// ---------- models ----------

type Logical struct {
	ID, Purpose, RecipientName, Amount, Currency string
	CreatedAt                                    time.Time
}

type Batch struct {
	ID            string    `json:"id"`
	SenderBatchID string    `json:"sender_batch_id"`
	PayPalBatchID string    `json:"paypal_batch_id"`
	RequestID     string    `json:"request_id"`
	Status        string    `json:"status"`
	Mode          string    `json:"mode"`
	CreatedAt     time.Time `json:"created_at"`
}

type Item struct {
	ID              string       `json:"id"`
	LogicalPayoutID string       `json:"logical_payout_id"`
	BatchID         string       `json:"batch_id"`
	SenderItemID    string       `json:"sender_item_id"`
	PayPalItemID    string       `json:"paypal_item_id"`
	RecipientEmail  string       `json:"recipient_email"`
	CorrectedEmail  string       `json:"corrected_email"`
	Attempt         int          `json:"attempt"`
	OriginalItemID  string       `json:"original_item_id"`
	Status          state.Status `json:"status"`
	ErrorCode       string       `json:"error_code"`
	SimTag          string       `json:"sim_tag"`
	Mode            string       `json:"mode"`
	NeedsReview     bool         `json:"needs_review"`
	Amount          string       `json:"amount"`
	Currency        string       `json:"currency"`
	RecipientName   string       `json:"recipient_name"`
	Purpose         string       `json:"purpose"`
	CreatedAt       time.Time    `json:"created_at"`
	UpdatedAt       time.Time    `json:"updated_at"`
	StatusChangedAt time.Time    `json:"status_changed_at"`
	ExpiresAt       time.Time    `json:"expires_at"`
	Version         int          `json:"version"`
	AgeDays         int          `json:"age_days"`
	DaysLeft        int          `json:"days_left"`
}

type Action struct {
	ID             string          `json:"id"`
	ItemID         string          `json:"item_id"`
	Kind           string          `json:"kind"`
	TemplateKey    string          `json:"template_key"`
	ProposedBy     string          `json:"proposed_by"`
	Reason         string          `json:"reason"`
	Status         string          `json:"status"`
	IdempotencyKey string          `json:"idempotency_key"`
	Payload        json.RawMessage `json:"payload"`
	DecidedBy      string          `json:"decided_by"`
	DecidedAt      *time.Time      `json:"decided_at"`
	ExecutedAt     *time.Time      `json:"executed_at"`
	Result         json.RawMessage `json:"result"`
	CreatedAt      time.Time       `json:"created_at"`
	SnoozedUntil   *time.Time      `json:"snoozed_until,omitempty"`
}

type Message struct {
	ID        string          `json:"id"`
	ItemID    string          `json:"item_id"`
	ActionID  string          `json:"action_id"`
	Direction string          `json:"direction"`
	Subject   string          `json:"subject"`
	Body      string          `json:"body"`
	Status    string          `json:"status"`
	Extracted json.RawMessage `json:"extracted"`
	Flags     json.RawMessage `json:"flags"`
	CreatedAt time.Time       `json:"created_at"`
}

type AuditEntry struct {
	ID       int64           `json:"id"`
	TS       time.Time       `json:"ts"`
	Actor    string          `json:"actor"`
	Event    string          `json:"event"`
	ItemID   string          `json:"item_id"`
	Details  json.RawMessage `json:"details"`
	PrevHash string          `json:"prev_hash"`
	Hash     string          `json:"hash"`
}

type EventRow struct {
	EventID, EventType, ResourceID string
	Verified                       bool
	Payload                        []byte
}

// ---------- inserts ----------

func InsertLogical(ctx context.Context, q Q, l *Logical) error {
	return q.QueryRowContext(ctx, `INSERT INTO logical_payouts(purpose, recipient_name, amount, currency) VALUES ($1,$2,$3,$4) RETURNING id, created_at`,
		l.Purpose, l.RecipientName, l.Amount, strings.ToUpper(l.Currency)).Scan(&l.ID, &l.CreatedAt)
}

func InsertBatch(ctx context.Context, q Q, b *Batch) error {
	return q.QueryRowContext(ctx, `INSERT INTO payout_batches(sender_batch_id, request_id, status, mode) VALUES ($1,$2,$3,$4) RETURNING id, created_at`,
		b.SenderBatchID, b.RequestID, nonEmpty(b.Status, "CREATED"), b.Mode).Scan(&b.ID, &b.CreatedAt)
}

func UpdateBatch(ctx context.Context, q Q, id, paypalBatchID, status string) error {
	_, err := q.ExecContext(ctx, `UPDATE payout_batches SET paypal_batch_id=NULLIF($2,''), status=$3 WHERE id=$1`, id, paypalBatchID, status)
	return err
}

func GetBatchBySender(ctx context.Context, q Q, senderBatchID string) (*Batch, error) {
	var b Batch
	err := q.QueryRowContext(ctx, `SELECT id, sender_batch_id, COALESCE(paypal_batch_id,''), request_id, status, mode, created_at FROM payout_batches WHERE sender_batch_id=$1`, senderBatchID).
		Scan(&b.ID, &b.SenderBatchID, &b.PayPalBatchID, &b.RequestID, &b.Status, &b.Mode, &b.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &b, err
}

func GetBatch(ctx context.Context, q Q, id string) (*Batch, error) {
	var b Batch
	err := q.QueryRowContext(ctx, `SELECT id, sender_batch_id, COALESCE(paypal_batch_id,''), request_id, status, mode, created_at FROM payout_batches WHERE id=$1`, id).
		Scan(&b.ID, &b.SenderBatchID, &b.PayPalBatchID, &b.RequestID, &b.Status, &b.Mode, &b.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &b, err
}

func ListBatches(ctx context.Context, q Q, limit int) ([]Batch, error) {
	rows, err := q.QueryContext(ctx, `SELECT b.id, b.sender_batch_id, COALESCE(b.paypal_batch_id,''), b.request_id, b.status, b.mode, b.created_at FROM payout_batches b ORDER BY b.created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Batch
	for rows.Next() {
		var b Batch
		if err := rows.Scan(&b.ID, &b.SenderBatchID, &b.PayPalBatchID, &b.RequestID, &b.Status, &b.Mode, &b.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// InsertItem inserts an item; a unique violation on the live-item index becomes ErrLiveItemExists.
func InsertItem(ctx context.Context, q Q, it *Item, createdAt time.Time) error {
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	err := q.QueryRowContext(ctx, `INSERT INTO payout_items(logical_payout_id,batch_id,sender_item_id,paypal_item_id,recipient_email,attempt,original_item_id,status,error_code,sim_tag,mode,created_at,updated_at,status_changed_at,expires_at)
		VALUES ($1,$2,$3,NULLIF($4,''),$5,$6,NULLIF($7,'')::uuid,$8,NULLIF($9,''),NULLIF($10,''),$11,$12,$12,$12,$12::timestamptz + interval '30 days') RETURNING id`,
		it.LogicalPayoutID, it.BatchID, it.SenderItemID, it.PayPalItemID, it.RecipientEmail, nonZero(it.Attempt, 1), it.OriginalItemID,
		string(it.Status), it.ErrorCode, it.SimTag, it.Mode, createdAt).Scan(&it.ID)
	var pe *pq.Error
	if errors.As(err, &pe) && pe.Code == "23505" && pe.Constraint == "one_live_item_per_payout" {
		return ErrLiveItemExists
	}
	return err
}

const itemSelect = `SELECT i.id, i.logical_payout_id, i.batch_id, i.sender_item_id, COALESCE(i.paypal_item_id,''), i.recipient_email,
	COALESCE(i.corrected_email,''), i.attempt, COALESCE(i.original_item_id::text,''), i.status, COALESCE(i.error_code,''), COALESCE(i.sim_tag,''),
	i.mode, i.needs_review, l.amount::text, l.currency, l.recipient_name, l.purpose, i.created_at, i.updated_at, i.status_changed_at, i.expires_at, i.version
	FROM payout_items i JOIN logical_payouts l ON l.id = i.logical_payout_id`

type scanner interface{ Scan(dest ...any) error }

func scanItem(r scanner, now time.Time) (*Item, error) {
	var it Item
	var st string
	err := r.Scan(&it.ID, &it.LogicalPayoutID, &it.BatchID, &it.SenderItemID, &it.PayPalItemID, &it.RecipientEmail, &it.CorrectedEmail, &it.Attempt,
		&it.OriginalItemID, &st, &it.ErrorCode, &it.SimTag, &it.Mode, &it.NeedsReview, &it.Amount, &it.Currency, &it.RecipientName, &it.Purpose,
		&it.CreatedAt, &it.UpdatedAt, &it.StatusChangedAt, &it.ExpiresAt, &it.Version)
	if err != nil {
		return nil, err
	}
	it.Status = state.Status(st)
	it.AgeDays = int(now.Sub(it.CreatedAt).Hours() / 24)
	it.DaysLeft = 30 - it.AgeDays
	if it.DaysLeft < 0 {
		it.DaysLeft = 0
	}
	return &it, nil
}

// GetItem loads an item; with forUpdate it locks the item row for the transaction.
func GetItem(ctx context.Context, q Q, id string, forUpdate bool) (*Item, error) {
	query := itemSelect + ` WHERE i.id = $1`
	if forUpdate {
		query += ` FOR UPDATE OF i`
	}
	it, err := scanItem(q.QueryRowContext(ctx, query, id), time.Now())
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return it, err
}

func FindItemByPayPalID(ctx context.Context, q Q, ppID string) (*Item, error) {
	it, err := scanItem(q.QueryRowContext(ctx, itemSelect+` WHERE i.paypal_item_id = $1`, ppID), time.Now())
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return it, err
}

type ItemFilter struct {
	Statuses []string
	Mode     string
	BatchID  string
	Search   string
	Limit    int
}

func ListItems(ctx context.Context, q Q, f ItemFilter) ([]Item, error) {
	var where []string
	var args []any
	add := func(cond string, v any) { args = append(args, v); where = append(where, fmt.Sprintf(cond, len(args))) }
	if len(f.Statuses) > 0 {
		add("i.status = ANY($%d)", pq.Array(f.Statuses))
	}
	if f.Mode != "" {
		add("i.mode = $%d", f.Mode)
	}
	if f.BatchID != "" {
		add("i.batch_id = $%d", f.BatchID)
	}
	if f.Search != "" {
		add("(l.recipient_name ILIKE '%%' || $%[1]d || '%%' OR i.recipient_email ILIKE '%%' || $%[1]d || '%%')", f.Search)
	}
	query := itemSelect
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	limit := nonZero(f.Limit, 500)
	query += fmt.Sprintf(" ORDER BY i.created_at DESC, i.id LIMIT %d", limit)
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := time.Now()
	var out []Item
	for rows.Next() {
		it, err := scanItem(rows, now)
		if err != nil {
			return nil, err
		}
		out = append(out, *it)
	}
	return out, rows.Err()
}

func UpdateItemStatus(ctx context.Context, q Q, id string, st state.Status, errCode string) error {
	_, err := q.ExecContext(ctx, `UPDATE payout_items SET
		status_changed_at = CASE WHEN status <> $2 THEN now() ELSE status_changed_at END,
		status = $2, error_code = NULLIF($3,''), updated_at = now(), version = version + 1 WHERE id = $1`, id, string(st), errCode)
	return err
}

func SetItemPayPalIDs(ctx context.Context, q Q, id, ppItemID string, st state.Status, errCode string) error {
	_, err := q.ExecContext(ctx, `UPDATE payout_items SET paypal_item_id = NULLIF($2,''), status=$3, error_code=NULLIF($4,''), updated_at=now(), status_changed_at=now(), version=version+1 WHERE id=$1`,
		id, ppItemID, string(st), errCode)
	return err
}

func SetNeedsReview(ctx context.Context, q Q, id string, v bool) error {
	_, err := q.ExecContext(ctx, `UPDATE payout_items SET needs_review=$2, updated_at=now() WHERE id=$1`, id, v)
	return err
}

func SetCorrectedEmail(ctx context.Context, q Q, id, email string) error {
	_, err := q.ExecContext(ctx, `UPDATE payout_items SET corrected_email=NULLIF($2,''), updated_at=now() WHERE id=$1`, id, email)
	return err
}

// BackdateItem moves an item's creation time into the past (FAKE items only; used to demo day 20/27/30).
func BackdateItem(ctx context.Context, q Q, id string, days int) error {
	_, err := q.ExecContext(ctx, `UPDATE payout_items SET created_at = created_at - make_interval(days => $2), expires_at = expires_at - make_interval(days => $2), updated_at=now() WHERE id=$1 AND mode='FAKE'`, id, days)
	return err
}

func ItemsNeedingPoll(ctx context.Context, q Q) ([]Item, error) {
	return ListItems(ctx, q, ItemFilter{Statuses: []string{"CREATED", "PENDING", "UNCLAIMED", "ONHOLD", "BLOCKED", "UNKNOWN"}, Limit: 2000})
}

// ---------- actions ----------

// InsertAction inserts an action unless one with the same idempotency key exists.
func InsertAction(ctx context.Context, q Q, a *Action) (bool, error) {
	if len(a.Payload) == 0 {
		a.Payload = json.RawMessage(`{}`)
	}
	err := q.QueryRowContext(ctx, `INSERT INTO actions(item_id,kind,template_key,proposed_by,reason,status,idempotency_key,payload)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (idempotency_key) DO NOTHING RETURNING id, created_at`,
		a.ItemID, a.Kind, a.TemplateKey, nonEmpty(a.ProposedBy, "rules"), a.Reason, nonEmpty(a.Status, "proposed"), a.IdempotencyKey, []byte(a.Payload)).
		Scan(&a.ID, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

const actionSelect = `SELECT id, item_id, kind, template_key, proposed_by, reason, status, idempotency_key, payload, COALESCE(decided_by,''), decided_at, executed_at, result, created_at, snoozed_until FROM actions`

func scanAction(r scanner) (*Action, error) {
	var a Action
	var payload, result []byte
	err := r.Scan(&a.ID, &a.ItemID, &a.Kind, &a.TemplateKey, &a.ProposedBy, &a.Reason, &a.Status, &a.IdempotencyKey, &payload, &a.DecidedBy, &a.DecidedAt, &a.ExecutedAt, &result, &a.CreatedAt, &a.SnoozedUntil)
	a.Payload, a.Result = payload, result
	return &a, err
}

func GetAction(ctx context.Context, q Q, id string, forUpdate bool) (*Action, error) {
	query := actionSelect + ` WHERE id=$1`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	a, err := scanAction(q.QueryRowContext(ctx, query, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

func ListActions(ctx context.Context, q Q, status, itemID string, limit int) ([]Action, error) {
	query := actionSelect + ` WHERE ($1='' OR status=$1) AND ($2='' OR item_id::text=$2) AND ($1!='proposed' OR $2!='' OR snoozed_until IS NULL OR snoozed_until <= now()) ORDER BY created_at DESC LIMIT $3`
	rows, err := q.QueryContext(ctx, query, status, itemID, nonZero(limit, 200))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Action
	for rows.Next() {
		a, err := scanAction(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

func ApprovedBefore(ctx context.Context, q Q, before time.Time) ([]Action, error) {
	rows, err := q.QueryContext(ctx, actionSelect+` WHERE status='approved' AND decided_at < $1 ORDER BY decided_at`, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Action
	for rows.Next() {
		a, err := scanAction(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

func SetActionStatus(ctx context.Context, q Q, id, status, decidedBy string, result any) error {
	var res []byte
	if result != nil {
		res, _ = json.Marshal(result)
	}
	_, err := q.ExecContext(ctx, `UPDATE actions SET status=$2,
		decided_by = CASE WHEN $3 <> '' THEN $3 ELSE decided_by END,
		decided_at = CASE WHEN $2 IN ('approved','rejected') THEN now() ELSE decided_at END,
		executed_at = CASE WHEN $2 IN ('executed','failed') THEN now() ELSE executed_at END,
		result = COALESCE($4::jsonb, result) WHERE id=$1`, id, status, decidedBy, nullBytes(res))
	return err
}

// ---------- messages ----------

func InsertMessage(ctx context.Context, q Q, m *Message) error {
	return q.QueryRowContext(ctx, `INSERT INTO messages(item_id, action_id, direction, subject, body, status, extracted, flags) VALUES ($1,NULLIF($2,'')::uuid,$3,$4,$5,$6,$7,$8) RETURNING id, created_at`,
		m.ItemID, m.ActionID, m.Direction, m.Subject, m.Body, nonEmpty(m.Status, "draft"), nullBytes(m.Extracted), nullBytes(m.Flags)).Scan(&m.ID, &m.CreatedAt)
}

func ListMessages(ctx context.Context, q Q, itemID string) ([]Message, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, item_id, COALESCE(action_id::text,''), direction, subject, body, status, extracted, flags, created_at FROM messages WHERE item_id=$1 ORDER BY created_at, id`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var ex, fl []byte
		if err := rows.Scan(&m.ID, &m.ItemID, &m.ActionID, &m.Direction, &m.Subject, &m.Body, &m.Status, &ex, &fl, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.Extracted, m.Flags = ex, fl
		out = append(out, m)
	}
	return out, rows.Err()
}

func SetMessageStatusByAction(ctx context.Context, q Q, actionID, status string) error {
	_, err := q.ExecContext(ctx, `UPDATE messages SET status=$2 WHERE action_id=$1`, actionID, status)
	return err
}

// ---------- webhook events ----------

// InsertWebhookEvent stores an event once. It returns false for a duplicate.
func InsertWebhookEvent(ctx context.Context, q Q, e EventRow) (bool, error) {
	res, err := q.ExecContext(ctx, `INSERT INTO webhook_events(event_id,event_type,resource_id,verified,payload) VALUES ($1,$2,$3,$4,$5) ON CONFLICT (event_id) DO NOTHING`,
		e.EventID, e.EventType, e.ResourceID, e.Verified, e.Payload)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func MarkEventProcessed(ctx context.Context, q Q, eventID, errMsg string) error {
	_, err := q.ExecContext(ctx, `UPDATE webhook_events SET processed_at=now(), error=NULLIF($2,'') WHERE event_id=$1`, eventID, errMsg)
	return err
}

func CountEvents(ctx context.Context, q Q) (total, unprocessed int, err error) {
	err = q.QueryRowContext(ctx, `SELECT count(*), count(*) FILTER (WHERE processed_at IS NULL) FROM webhook_events`).Scan(&total, &unprocessed)
	return
}

// ---------- audit (append-only, hash-chained) ----------

func auditHash(prev string, ts time.Time, actor, event, itemID string, details []byte) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%s|%s|%s|", prev, ts.UTC().Format(time.RFC3339Nano), actor, event, itemID)
	h.Write(details)
	return hex.EncodeToString(h.Sum(nil))
}

// AppendAudit adds a hash-chained audit entry inside tx.
func AppendAudit(ctx context.Context, tx *sql.Tx, actor, event, itemID string, details any) error {
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(7002)`); err != nil {
		return err
	}
	prev := "genesis"
	_ = tx.QueryRowContext(ctx, `SELECT hash FROM audit_log ORDER BY id DESC LIMIT 1`).Scan(&prev)
	d := []byte(`{}`)
	if details != nil {
		d, _ = json.Marshal(details)
	}
	ts := time.Now().UTC().Truncate(time.Microsecond)
	hash := auditHash(prev, ts, actor, event, itemID, d)
	_, err := tx.ExecContext(ctx, `INSERT INTO audit_log(ts,actor,event,item_id,details,prev_hash,hash) VALUES ($1,$2,$3,NULLIF($4,'')::uuid,$5,$6,$7)`,
		ts, actor, event, itemID, d, prev, hash)
	return err
}

func ListAudit(ctx context.Context, q Q, itemID string, limit int) ([]AuditEntry, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, ts, actor, event, COALESCE(item_id::text,''), details, prev_hash, hash FROM audit_log WHERE ($1='' OR item_id::text=$1) ORDER BY id DESC LIMIT $2`, itemID, nonZero(limit, 200))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var d []byte
		if err := rows.Scan(&e.ID, &e.TS, &e.Actor, &e.Event, &e.ItemID, &d, &e.PrevHash, &e.Hash); err != nil {
			return nil, err
		}
		e.Details = d
		out = append(out, e)
	}
	return out, rows.Err()
}

// VerifyAudit walks the chain from the start; it returns the id of the first broken entry (0 if intact).
func VerifyAudit(ctx context.Context, q Q) (entries int, brokenAt int64, err error) {
	rows, err := q.QueryContext(ctx, `SELECT id, ts, actor, event, COALESCE(item_id::text,''), details::text, prev_hash, hash FROM audit_log ORDER BY id`)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	prev := "genesis"
	for rows.Next() {
		var id int64
		var ts time.Time
		var actor, event, item, details, ph, h string
		if err := rows.Scan(&id, &ts, &actor, &event, &item, &details, &ph, &h); err != nil {
			return entries, 0, err
		}
		entries++
		// details::text re-serialises JSONB, so compare using the canonical form.
		var canon any
		_ = json.Unmarshal([]byte(details), &canon)
		cb, _ := json.Marshal(canon)
		if ph != prev || (auditHash(prev, ts, actor, event, item, []byte(details)) != h && auditHash(prev, ts, actor, event, item, cb) != h) {
			return entries, id, nil
		}
		prev = h
	}
	return entries, 0, rows.Err()
}

// ---------- bench ----------

func SaveBench(ctx context.Context, q Q, result []byte) error {
	_, err := q.ExecContext(ctx, `INSERT INTO bench_runs(result) VALUES ($1)`, result)
	return err
}

func LatestBench(ctx context.Context, q Q) ([]byte, time.Time, error) {
	var b []byte
	var t time.Time
	err := q.QueryRowContext(ctx, `SELECT result, created_at FROM bench_runs ORDER BY id DESC LIMIT 1`).Scan(&b, &t)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, t, ErrNotFound
	}
	return b, t, err
}

func CountAuditEvent(ctx context.Context, q Q, event string) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM audit_log WHERE event=$1`, event).Scan(&n)
	return n, err
}

func nonEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func nonZero(n, def int) int {
	if n == 0 {
		return def
	}
	return n
}

func nullBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// ---------- additional queries ----------

func GetItemBySender(ctx context.Context, q Q, senderItemID string) (*Item, error) {
	it, err := scanItem(q.QueryRowContext(ctx, itemSelect+` WHERE i.sender_item_id = $1`, senderItemID), time.Now())
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return it, err
}

func GetLogical(ctx context.Context, q Q, id string) (*Logical, error) {
	var l Logical
	err := q.QueryRowContext(ctx, `SELECT id, purpose, recipient_name, amount::text, currency, created_at FROM logical_payouts WHERE id=$1`, id).
		Scan(&l.ID, &l.Purpose, &l.RecipientName, &l.Amount, &l.Currency, &l.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &l, err
}

// BackdateFake moves every FAKE item back in time so day-20/27/30 behaviour can be demonstrated.
func BackdateFake(ctx context.Context, q Q, days int) error {
	_, err := q.ExecContext(ctx, `UPDATE payout_items SET created_at = created_at - make_interval(days => $1), expires_at = expires_at - make_interval(days => $1), updated_at = now() WHERE mode='FAKE'`, days)
	return err
}

type StoredEvent struct {
	EventID, EventType, ResourceID string
	Payload                        []byte
	Processed                      bool
}

func GetEvent(ctx context.Context, q Q, id string) (*StoredEvent, error) {
	var e StoredEvent
	var processed sql.NullTime
	err := q.QueryRowContext(ctx, `SELECT event_id, event_type, resource_id, payload, processed_at FROM webhook_events WHERE event_id=$1`, id).
		Scan(&e.EventID, &e.EventType, &e.ResourceID, &e.Payload, &processed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	e.Processed = processed.Valid
	return &e, err
}

func UnprocessedEvents(ctx context.Context, q Q, olderThan time.Time) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT event_id FROM webhook_events WHERE processed_at IS NULL AND received_at < $1 ORDER BY received_at LIMIT 200`, olderThan)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// BatchesCreatedBefore returns batches whose creation call never completed.
func BatchesCreatedBefore(ctx context.Context, q Q, before time.Time) ([]Batch, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, sender_batch_id, COALESCE(paypal_batch_id,''), request_id, status, mode, created_at FROM payout_batches WHERE status='CREATED' AND paypal_batch_id IS NULL AND mode IN ('LIVE_SANDBOX','FAKE') AND created_at < $1`, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Batch
	for rows.Next() {
		var b Batch
		if err := rows.Scan(&b.ID, &b.SenderBatchID, &b.PayPalBatchID, &b.RequestID, &b.Status, &b.Mode, &b.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func GetBatchByPayPalID(ctx context.Context, q Q, ppID string) (*Batch, error) {
	var b Batch
	err := q.QueryRowContext(ctx, `SELECT id, sender_batch_id, COALESCE(paypal_batch_id,''), request_id, status, mode, created_at FROM payout_batches WHERE paypal_batch_id=$1`, ppID).
		Scan(&b.ID, &b.SenderBatchID, &b.PayPalBatchID, &b.RequestID, &b.Status, &b.Mode, &b.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &b, err
}

// RecentDuplicates finds earlier payouts to the same email, amount and currency since a time.
func RecentDuplicates(ctx context.Context, q Q, email, amount, currency string, since time.Time) ([]Item, error) {
	rows, err := q.QueryContext(ctx, itemSelect+` WHERE (lower(i.recipient_email)=lower($1) OR lower(i.corrected_email)=lower($1)) AND l.amount = $2::numeric AND l.currency = upper($3) AND i.created_at >= $4 AND i.status <> 'FAILED' ORDER BY i.created_at DESC LIMIT 3`,
		email, amount, currency, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := time.Now()
	var out []Item
	for rows.Next() {
		it, err := scanItem(rows, now)
		if err != nil {
			return nil, err
		}
		out = append(out, *it)
	}
	return out, rows.Err()
}
