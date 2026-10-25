package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

// ---------- conflicts ----------

type Conflict struct {
	ID         string          `json:"id"`
	ItemID     string          `json:"item_id"`
	Kind       string          `json:"kind"`
	Summary    string          `json:"summary"`
	Details    json.RawMessage `json:"details"`
	Status     string          `json:"status"`
	Resolution *string         `json:"resolution"`
	ResolvedBy *string         `json:"resolved_by"`
	Note       *string         `json:"note"`
	DedupeKey  string          `json:"dedupe_key"`
	CreatedAt  time.Time       `json:"created_at"`
	ResolvedAt *time.Time      `json:"resolved_at"`
}

func InsertConflict(ctx context.Context, q Q, c *Conflict) (bool, error) {
	if len(c.Details) == 0 {
		c.Details = json.RawMessage(`{}`)
	}
	err := q.QueryRowContext(ctx,
		`INSERT INTO conflicts(item_id, kind, summary, details, dedupe_key)
		 VALUES ($1,$2,$3,$4,$5)
		 ON CONFLICT (dedupe_key) DO NOTHING
		 RETURNING id, created_at`,
		c.ItemID, c.Kind, c.Summary, []byte(c.Details), c.DedupeKey,
	).Scan(&c.ID, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

const conflictSelect = `SELECT id, item_id, kind, summary, details, status,
	resolution, resolved_by, note, dedupe_key, created_at, resolved_at
	FROM conflicts`

func scanConflict(r scanner) (*Conflict, error) {
	var c Conflict
	var details []byte
	err := r.Scan(&c.ID, &c.ItemID, &c.Kind, &c.Summary, &details, &c.Status,
		&c.Resolution, &c.ResolvedBy, &c.Note, &c.DedupeKey, &c.CreatedAt, &c.ResolvedAt)
	c.Details = details
	return &c, err
}

func GetConflict(ctx context.Context, q Q, id string) (*Conflict, error) {
	c, err := scanConflict(q.QueryRowContext(ctx, conflictSelect+` WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

func ListConflicts(ctx context.Context, q Q, status string, limit int) ([]Conflict, error) {
	query := conflictSelect
	var args []any
	if status != "" {
		query += ` WHERE status=$1`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC LIMIT ` + strconv.Itoa(nonZero(limit, 200))
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Conflict
	for rows.Next() {
		c, err := scanConflict(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func ListConflictsByItem(ctx context.Context, q Q, itemID string) ([]Conflict, error) {
	rows, err := q.QueryContext(ctx, conflictSelect+` WHERE item_id=$1 ORDER BY created_at DESC`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Conflict
	for rows.Next() {
		c, err := scanConflict(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func ResolveConflict(ctx context.Context, q Q, id, resolution, resolvedBy, note string) error {
	_, err := q.ExecContext(ctx,
		`UPDATE conflicts SET status='resolved', resolution=$2, resolved_by=$3,
		 note=NULLIF($4,''), resolved_at=now() WHERE id=$1`,
		id, resolution, resolvedBy, note)
	return err
}

func CountOpenConflicts(ctx context.Context, q Q) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM conflicts WHERE status='open'`).Scan(&n)
	return n, err
}

// ---------- notes ----------

type Note struct {
	ID        string    `json:"id"`
	ItemID    string    `json:"item_id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

func InsertNote(ctx context.Context, q Q, n *Note) error {
	return q.QueryRowContext(ctx,
		`INSERT INTO notes(item_id, author, body) VALUES ($1,$2,$3) RETURNING id, created_at`,
		n.ItemID, n.Author, n.Body).Scan(&n.ID, &n.CreatedAt)
}

func ListNotes(ctx context.Context, q Q, itemID string) ([]Note, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id, item_id, COALESCE(author,''), COALESCE(body,''), created_at FROM notes WHERE item_id=$1 ORDER BY created_at`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Note
	for rows.Next() {
		var n Note
		if err := rows.Scan(&n.ID, &n.ItemID, &n.Author, &n.Body, &n.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
