// Package store keeps bot state in SQLite: drafts, usage counters,
// the Firefly entity cache and a log of transactions created by the bot.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver
)

// Store is a SQLite-backed state store.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

const schema = `
CREATE TABLE IF NOT EXISTS drafts (
	id         TEXT PRIMARY KEY,
	user_id    INTEGER NOT NULL,
	awaiting   TEXT NOT NULL DEFAULT '',
	data       BLOB NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS drafts_user ON drafts(user_id, updated_at);
CREATE TABLE IF NOT EXISTS usage (
	kind      TEXT NOT NULL,
	name      TEXT NOT NULL,
	count     INTEGER NOT NULL DEFAULT 0,
	last_used INTEGER NOT NULL,
	PRIMARY KEY (kind, name)
);
CREATE TABLE IF NOT EXISTS cache (
	kind       TEXT PRIMARY KEY,
	data       BLOB NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS created (
	group_id   TEXT PRIMARY KEY,
	user_id    INTEGER NOT NULL,
	chat_id    INTEGER NOT NULL DEFAULT 0,
	message_id INTEGER NOT NULL DEFAULT 0,
	summary    TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS cards (
	chat_id    INTEGER NOT NULL,
	message_id INTEGER NOT NULL,
	group_id   TEXT NOT NULL,
	PRIMARY KEY (chat_id, message_id)
);
`

// Open opens (creating if needed) the database in dir.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.Join(dir, "tsuke.db") +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	return open(dsn)
}

func open(dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return &Store{db: db, now: time.Now}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("store: not found")

// Draft is a persisted draft payload.
type Draft struct {
	ID        string
	UserID    int64
	Awaiting  string
	Data      []byte
	UpdatedAt time.Time
}

// PutDraft inserts or replaces a draft.
func (s *Store) PutDraft(ctx context.Context, d Draft) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO drafts(id,user_id,awaiting,data,updated_at) VALUES(?,?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET awaiting=excluded.awaiting, data=excluded.data, updated_at=excluded.updated_at`,
		d.ID, d.UserID, d.Awaiting, d.Data, s.now().Unix())
	return err
}

// Draft loads a draft by id.
func (s *Store) Draft(ctx context.Context, id string) (Draft, error) {
	d := Draft{ID: id}
	var ts int64
	err := s.db.QueryRowContext(ctx, `SELECT user_id,awaiting,data,updated_at FROM drafts WHERE id=?`, id).
		Scan(&d.UserID, &d.Awaiting, &d.Data, &ts)
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	d.UpdatedAt = time.Unix(ts, 0)
	return d, err
}

// AwaitingDraft returns the user's most recently updated draft that waits for text input.
func (s *Store) AwaitingDraft(ctx context.Context, userID int64) (Draft, error) {
	d := Draft{UserID: userID}
	var ts int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id,awaiting,data,updated_at FROM drafts WHERE user_id=? AND awaiting<>'' ORDER BY updated_at DESC, rowid DESC LIMIT 1`, userID).
		Scan(&d.ID, &d.Awaiting, &d.Data, &ts)
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	d.UpdatedAt = time.Unix(ts, 0)
	return d, err
}

// LatestDraft returns the user's most recently updated draft.
func (s *Store) LatestDraft(ctx context.Context, userID int64) (Draft, error) {
	d := Draft{UserID: userID}
	var ts int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id,awaiting,data,updated_at FROM drafts WHERE user_id=? ORDER BY updated_at DESC, rowid DESC LIMIT 1`, userID).
		Scan(&d.ID, &d.Awaiting, &d.Data, &ts)
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	d.UpdatedAt = time.Unix(ts, 0)
	return d, err
}

// DeleteDraft removes a draft.
func (s *Store) DeleteDraft(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM drafts WHERE id=?`, id)
	return err
}

// PurgeDrafts deletes drafts not touched since before and returns their ids.
func (s *Store) PurgeDrafts(ctx context.Context, before time.Time) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM drafts WHERE updated_at<?`, before.Unix())
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM drafts WHERE updated_at<?`, before.Unix())
	return ids, err
}

// Bump records one use of a named entity (kind: asset, expense, revenue, category, budget, tag).
func (s *Store) Bump(ctx context.Context, kind, name string) error {
	if name == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO usage(kind,name,count,last_used) VALUES(?,?,1,?)
		 ON CONFLICT(kind,name) DO UPDATE SET count=count+1, last_used=excluded.last_used`,
		kind, name, s.now().Unix())
	return err
}

// Usage returns use counts for a kind.
func (s *Store) Usage(ctx context.Context, kind string) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name,count FROM usage WHERE kind=?`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var n string
		var c int
		if err := rows.Scan(&n, &c); err != nil {
			return nil, err
		}
		out[n] = c
	}
	return out, rows.Err()
}

// PutCache stores a cache blob.
func (s *Store) PutCache(ctx context.Context, kind string, data []byte) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO cache(kind,data,updated_at) VALUES(?,?,?)
		 ON CONFLICT(kind) DO UPDATE SET data=excluded.data, updated_at=excluded.updated_at`,
		kind, data, s.now().Unix())
	return err
}

// Cache loads a cache blob and its age.
func (s *Store) Cache(ctx context.Context, kind string) ([]byte, time.Time, error) {
	var data []byte
	var ts int64
	err := s.db.QueryRowContext(ctx, `SELECT data,updated_at FROM cache WHERE kind=?`, kind).Scan(&data, &ts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, time.Time{}, ErrNotFound
	}
	return data, time.Unix(ts, 0), err
}

// Created is a transaction created (or adopted) by the bot.
type Created struct {
	GroupID   string
	UserID    int64
	ChatID    int64
	MessageID int
	Summary   string
	At        time.Time
}

// PutCreated records or updates a created transaction.
func (s *Store) PutCreated(ctx context.Context, c Created) error {
	at := c.At
	if at.IsZero() {
		at = s.now()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO created(group_id,user_id,chat_id,message_id,summary,created_at) VALUES(?,?,?,?,?,?)
		 ON CONFLICT(group_id) DO UPDATE SET chat_id=excluded.chat_id, message_id=excluded.message_id, summary=excluded.summary`,
		c.GroupID, c.UserID, c.ChatID, c.MessageID, c.Summary, at.Unix())
	if err != nil || c.MessageID == 0 {
		return err
	}
	return s.PutCard(ctx, c.ChatID, c.MessageID, c.GroupID)
}

// PutCard remembers that a bot message shows a transaction.
func (s *Store) PutCard(ctx context.Context, chatID int64, msgID int, groupID string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO cards(chat_id,message_id,group_id) VALUES(?,?,?)
		 ON CONFLICT(chat_id,message_id) DO UPDATE SET group_id=excluded.group_id`, chatID, msgID, groupID)
	return err
}

// GroupByCard returns the transaction group shown on a bot message.
func (s *Store) GroupByCard(ctx context.Context, chatID int64, msgID int) (string, error) {
	var g string
	err := s.db.QueryRowContext(ctx, `SELECT group_id FROM cards WHERE chat_id=? AND message_id=?`, chatID, msgID).Scan(&g)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return g, err
}

// LastCreated returns the newest created transaction of a user.
func (s *Store) LastCreated(ctx context.Context, userID int64) (Created, error) {
	return s.scanCreated(s.db.QueryRowContext(ctx,
		`SELECT group_id,user_id,chat_id,message_id,summary,created_at FROM created WHERE user_id=? ORDER BY created_at DESC, rowid DESC LIMIT 1`, userID))
}

func (s *Store) scanCreated(row *sql.Row) (Created, error) {
	var c Created
	var ts int64
	err := row.Scan(&c.GroupID, &c.UserID, &c.ChatID, &c.MessageID, &c.Summary, &ts)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	c.At = time.Unix(ts, 0)
	return c, err
}

// DeleteCreated forgets a created transaction.
func (s *Store) DeleteCreated(ctx context.Context, groupID string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM cards WHERE group_id=?`, groupID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM created WHERE group_id=?`, groupID)
	return err
}
