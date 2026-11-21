// roomcord-bridge-host - A host process for the mautrix bridgev2 network connectors.
// Copyright (C) 2026 The roomcord-bridge-host authors
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package store is the host's own SQLite database: the per-login event outbox and
// the materialized conversation/message views that back the read endpoints.
// Upstream bridge state lives in the per-network bridgev2 databases, not here.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "go.mau.fi/util/dbutil/litestream"

	"github.com/fulldiveVR/roomcord-bridge-host/internal/wire"
)

const schema = `
CREATE TABLE IF NOT EXISTS login_seq (
	login_id TEXT PRIMARY KEY,
	last_seq INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS outbox (
	login_id   TEXT    NOT NULL,
	seq        INTEGER NOT NULL,
	type       TEXT    NOT NULL,
	payload    TEXT    NOT NULL,
	created_at INTEGER NOT NULL,
	PRIMARY KEY (login_id, seq)
);
CREATE TABLE IF NOT EXISTS conversations (
	login_id   TEXT    NOT NULL,
	conv_id    TEXT    NOT NULL,
	payload    TEXT    NOT NULL,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (login_id, conv_id)
);
CREATE TABLE IF NOT EXISTS messages (
	login_id TEXT    NOT NULL,
	conv_id  TEXT    NOT NULL,
	msg_id   TEXT    NOT NULL,
	sent_at  INTEGER NOT NULL,
	payload  TEXT    NOT NULL,
	PRIMARY KEY (login_id, conv_id, msg_id)
);
CREATE INDEX IF NOT EXISTS messages_history_idx ON messages (login_id, conv_id, sent_at DESC, msg_id DESC);
CREATE TABLE IF NOT EXISTS sends (
	login_id            TEXT    NOT NULL,
	client_message_id   TEXT    NOT NULL,
	external_message_id TEXT    NOT NULL,
	sent_at             INTEGER NOT NULL,
	attachments         TEXT    NOT NULL DEFAULT '[]',
	PRIMARY KEY (login_id, client_message_id)
);
CREATE TABLE IF NOT EXISTS media (
	media_id   TEXT PRIMARY KEY,
	login_id   TEXT NOT NULL,
	mime_type  TEXT NOT NULL,
	file_name  TEXT NOT NULL,
	size       INTEGER NOT NULL,
	created_at INTEGER NOT NULL
);
`

type Store struct {
	db *sql.DB
	mu sync.Mutex

	notifyMu sync.Mutex
	notify   map[string]chan struct{}
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite3-fk-wal", fmt.Sprintf("file:%s?_txlock=immediate", path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create host schema: %w", err)
	}
	if _, err = db.Exec(`ALTER TABLE sends ADD COLUMN attachments TEXT NOT NULL DEFAULT '[]'`); err != nil && !strings.Contains(err.Error(), "duplicate column") {
		_ = db.Close()
		return nil, fmt.Errorf("migrate sends: %w", err)
	}
	return &Store{db: db, notify: make(map[string]chan struct{})}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Changed returns a channel that is closed on the next Emit for the login.
func (s *Store) Changed(loginID string) <-chan struct{} {
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	ch, ok := s.notify[loginID]
	if !ok {
		ch = make(chan struct{})
		s.notify[loginID] = ch
	}
	return ch
}

func (s *Store) wake(loginID string) {
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	if ch, ok := s.notify[loginID]; ok {
		close(ch)
		delete(s.notify, loginID)
	}
}

// Emit appends one event to the login's outbox with the next seq and updates the
// materialized views in the same transaction.
func (s *Store) Emit(ctx context.Context, loginID, evtType string, payload any) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var seq int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO login_seq (login_id, last_seq) VALUES ($1, 1)
		ON CONFLICT (login_id) DO UPDATE SET last_seq = login_seq.last_seq + 1
		RETURNING last_seq`, loginID).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("next seq: %w", err)
	}
	now := time.Now().UnixMilli()
	// materialize may complete the payload (full reaction list, unread count), so it runs first.
	if err = materialize(ctx, tx, loginID, evtType, payload, now); err != nil {
		return 0, fmt.Errorf("materialize %s: %w", evtType, err)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO outbox (login_id, seq, type, payload, created_at) VALUES ($1, $2, $3, $4, $5)`,
		loginID, seq, evtType, string(raw), now); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	s.wake(loginID)
	return seq, nil
}

func materialize(ctx context.Context, tx *sql.Tx, loginID, evtType string, payload any, now int64) error {
	switch p := payload.(type) {
	case *wire.ConversationUpsertPayload:
		var existing wire.Conversation
		found, err := getJSON(ctx, tx, &existing, `SELECT payload FROM conversations WHERE login_id=$1 AND conv_id=$2`, loginID, p.Conversation.ExternalConversationID)
		if err != nil {
			return err
		}
		conv := p.Conversation
		if found {
			if conv.Title == "" {
				conv.Title = existing.Title
			}
			if conv.Kind == "" {
				conv.Kind = existing.Kind
			}
			if conv.AvatarURL == nil {
				conv.AvatarURL = existing.AvatarURL
			}
			conv.ParticipantExternalIDs = union(existing.ParticipantExternalIDs, conv.ParticipantExternalIDs)
		}
		return putConversation(ctx, tx, loginID, &conv, now)
	case *wire.ParticipantUpsertPayload:
		var existing wire.Conversation
		found, err := getJSON(ctx, tx, &existing, `SELECT payload FROM conversations WHERE login_id=$1 AND conv_id=$2`, loginID, p.ExternalConversationID)
		if err != nil || !found {
			return err
		}
		existing.ParticipantExternalIDs = union(existing.ParticipantExternalIDs, []string{p.Participant.ExternalParticipantID})
		return putConversation(ctx, tx, loginID, &existing, now)
	case *wire.MessageUpsertPayload:
		msg := p.Message
		var existing wire.Message
		found, err := getJSON(ctx, tx, &existing, `SELECT payload FROM messages WHERE login_id=$1 AND conv_id=$2 AND msg_id=$3`,
			loginID, msg.ExternalConversationID, msg.ExternalMessageID)
		if err != nil {
			return err
		}
		if found {
			msg.Reactions = existing.Reactions
			if msg.SentAt.IsZero() {
				msg.SentAt = existing.SentAt
			}
		}
		return putMessage(ctx, tx, loginID, &msg)
	case *wire.MessageDeletePayload:
		return updateMessage(ctx, tx, loginID, p.ExternalConversationID, p.ExternalMessageID, func(m *wire.Message) {
			m.DeletedAt = &p.DeletedAt
		})
	case *wire.ReactionPayload:
		apply := func(current []wire.Reaction) []wire.Reaction {
			out := make([]wire.Reaction, 0, len(current)+1)
			for _, r := range current {
				if r.SenderExternalID != p.Reaction.SenderExternalID || !sameEmoji(r.Emoji, p.Reaction.Emoji) {
					out = append(out, r)
				}
			}
			if evtType == wire.EventReactionUpsert {
				out = append(out, p.Reaction)
			}
			return out
		}
		p.Reactions = apply(nil)
		return updateMessage(ctx, tx, loginID, p.ExternalConversationID, p.ExternalMessageID, func(m *wire.Message) {
			m.Reactions = apply(m.Reactions)
			p.Reactions = m.Reactions
		})
	case *wire.ReadReceiptPayload:
		if !p.IsSelf {
			return nil
		}
		var unread int
		err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM messages
			WHERE login_id=$1 AND conv_id=$2 AND json_extract(payload, '$.direction')='inbound'
			  AND sent_at > COALESCE((SELECT sent_at FROM messages WHERE login_id=$1 AND conv_id=$2 AND msg_id=$3), 0)`,
			loginID, p.ExternalConversationID, p.LastReadExternalMessageID).Scan(&unread)
		if err != nil {
			return err
		}
		p.UnreadCount = &unread
		var conv wire.Conversation
		found, err := getJSON(ctx, tx, &conv, `SELECT payload FROM conversations WHERE login_id=$1 AND conv_id=$2`, loginID, p.ExternalConversationID)
		if err != nil || !found {
			return err
		}
		lastRead := p.LastReadExternalMessageID
		conv.LastReadExternalMessageID = &lastRead
		conv.UnreadCount = &unread
		return putConversation(ctx, tx, loginID, &conv, now)
	}
	return nil
}

// sameEmoji ignores variation selectors: bridgev2 adds U+FE0F on some paths and not on others.
func sameEmoji(a, b string) bool {
	return strings.ReplaceAll(a, "\ufe0f", "") == strings.ReplaceAll(b, "\ufe0f", "")
}

func union(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, v := range append(append([]string{}, a...), b...) {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func getJSON(ctx context.Context, tx *sql.Tx, into any, query string, args ...any) (bool, error) {
	var raw string
	err := tx.QueryRowContext(ctx, query, args...).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return true, json.Unmarshal([]byte(raw), into)
}

func putConversation(ctx context.Context, tx *sql.Tx, loginID string, conv *wire.Conversation, now int64) error {
	raw, _ := json.Marshal(conv)
	_, err := tx.ExecContext(ctx, `
		INSERT INTO conversations (login_id, conv_id, payload, updated_at) VALUES ($1, $2, $3, $4)
		ON CONFLICT (login_id, conv_id) DO UPDATE SET payload=excluded.payload, updated_at=excluded.updated_at`,
		loginID, conv.ExternalConversationID, string(raw), now)
	return err
}

func putMessage(ctx context.Context, tx *sql.Tx, loginID string, msg *wire.Message) error {
	if msg.Reactions == nil {
		msg.Reactions = []wire.Reaction{}
	}
	if msg.Attachments == nil {
		msg.Attachments = []wire.Attachment{}
	}
	raw, _ := json.Marshal(msg)
	_, err := tx.ExecContext(ctx, `
		INSERT INTO messages (login_id, conv_id, msg_id, sent_at, payload) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (login_id, conv_id, msg_id) DO UPDATE SET payload=excluded.payload, sent_at=excluded.sent_at`,
		loginID, msg.ExternalConversationID, msg.ExternalMessageID, msg.SentAt.UnixMilli(), string(raw))
	return err
}

func updateMessage(ctx context.Context, tx *sql.Tx, loginID, convID, msgID string, fn func(*wire.Message)) error {
	var msg wire.Message
	found, err := getJSON(ctx, tx, &msg, `SELECT payload FROM messages WHERE login_id=$1 AND conv_id=$2 AND msg_id=$3`, loginID, convID, msgID)
	if err != nil || !found {
		return err
	}
	fn(&msg)
	return putMessage(ctx, tx, loginID, &msg)
}

func (s *Store) ReadEvents(ctx context.Context, loginID string, afterSeq int64, limit int) ([]wire.Frame, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT seq, type, payload FROM outbox WHERE login_id=$1 AND seq>$2 ORDER BY seq LIMIT $3`, loginID, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []wire.Frame
	for rows.Next() {
		var f wire.Frame
		var raw string
		if err = rows.Scan(&f.Seq, &f.Type, &raw); err != nil {
			return nil, err
		}
		f.Payload = json.RawMessage(raw)
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) HeadSeq(ctx context.Context, loginID string) (int64, error) {
	var seq int64
	err := s.db.QueryRowContext(ctx, `SELECT last_seq FROM login_seq WHERE login_id=$1`, loginID).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return seq, err
}

func (s *Store) ListConversations(ctx context.Context, loginID string, cursor string, limit int) ([]json.RawMessage, *string, error) {
	offset, _ := strconv.Atoi(cursor)
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM conversations WHERE login_id=$1 ORDER BY updated_at DESC, conv_id LIMIT $2 OFFSET $3`,
		loginID, limit+1, offset)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	items := make([]json.RawMessage, 0, limit)
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return nil, nil, err
		}
		items = append(items, json.RawMessage(raw))
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		n := strconv.Itoa(offset + limit)
		next = &n
	}
	return items, next, rows.Err()
}

func (s *Store) ConversationExists(ctx context.Context, loginID, convID string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM conversations WHERE login_id=$1 AND conv_id=$2`, loginID, convID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// History pages from newest to oldest. The cursor is "<sentAtMillis>|<msgId>" of the last returned item.
func (s *Store) History(ctx context.Context, loginID, convID, cursor string, limit int) ([]json.RawMessage, *string, error) {
	query := `SELECT sent_at, msg_id, payload FROM messages WHERE login_id=$1 AND conv_id=$2`
	args := []any{loginID, convID}
	if cursor != "" {
		ts, msgID, ok := strings.Cut(cursor, "|")
		tsInt, err := strconv.ParseInt(ts, 10, 64)
		if !ok || err != nil {
			return nil, nil, fmt.Errorf("invalid cursor")
		}
		query += ` AND (sent_at < $3 OR (sent_at = $3 AND msg_id < $4))`
		args = append(args, tsInt, msgID)
	}
	query += fmt.Sprintf(` ORDER BY sent_at DESC, msg_id DESC LIMIT %d`, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	type row struct {
		ts  int64
		id  string
		raw string
	}
	var all []row
	for rows.Next() {
		var r row
		if err = rows.Scan(&r.ts, &r.id, &r.raw); err != nil {
			return nil, nil, err
		}
		all = append(all, r)
	}
	var next *string
	if len(all) > limit {
		all = all[:limit]
		last := all[len(all)-1]
		n := fmt.Sprintf("%d|%s", last.ts, last.id)
		next = &n
	}
	items := make([]json.RawMessage, len(all))
	for i, r := range all {
		items[i] = json.RawMessage(r.raw)
	}
	return items, next, rows.Err()
}

func (s *Store) GetSend(ctx context.Context, loginID, clientMessageID string) (*wire.SendResponse, error) {
	var extID, rawAttachments string
	var sentAt int64
	err := s.db.QueryRowContext(ctx, `SELECT external_message_id, sent_at, attachments FROM sends WHERE login_id=$1 AND client_message_id=$2`,
		loginID, clientMessageID).Scan(&extID, &sentAt, &rawAttachments)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	resp := &wire.SendResponse{ExternalMessageID: extID, SentAt: time.UnixMilli(sentAt).UTC()}
	if err = json.Unmarshal([]byte(rawAttachments), &resp.Attachments); err != nil {
		return nil, err
	}
	if len(resp.Attachments) == 0 {
		resp.Attachments = nil
	}
	return resp, nil
}

func (s *Store) PutSend(ctx context.Context, loginID, clientMessageID string, resp *wire.SendResponse) error {
	attachments := resp.Attachments
	if attachments == nil {
		attachments = []wire.Attachment{}
	}
	rawAttachments, err := json.Marshal(attachments)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.ExecContext(ctx, `INSERT INTO sends (login_id, client_message_id, external_message_id, sent_at, attachments) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT DO NOTHING`, loginID, clientMessageID, resp.ExternalMessageID, resp.SentAt.UnixMilli(), string(rawAttachments))
	return err
}

// Redeliver appends a copy of the newest message_upsert frame of the login with a new
// seq, without touching the materialized views. It returns 0 when there is none.
func (s *Store) Redeliver(ctx context.Context, loginID string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var raw string
	err = tx.QueryRowContext(ctx, `SELECT payload FROM outbox WHERE login_id=$1 AND type=$2 ORDER BY seq DESC LIMIT 1`,
		loginID, wire.EventMessageUpsert).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	} else if err != nil {
		return 0, err
	}
	var seq int64
	if err = tx.QueryRowContext(ctx, `UPDATE login_seq SET last_seq = last_seq + 1 WHERE login_id=$1 RETURNING last_seq`, loginID).Scan(&seq); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO outbox (login_id, seq, type, payload, created_at) VALUES ($1, $2, $3, $4, $5)`,
		loginID, seq, wire.EventMessageUpsert, raw, time.Now().UnixMilli()); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	s.wake(loginID)
	return seq, nil
}

type Media struct {
	ID       string
	LoginID  string
	MimeType string
	FileName string
	Size     int64
}

func (s *Store) PutMedia(ctx context.Context, m *Media) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO media (media_id, login_id, mime_type, file_name, size, created_at) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (media_id) DO NOTHING`, m.ID, m.LoginID, m.MimeType, m.FileName, m.Size, time.Now().UnixMilli())
	return err
}

func (s *Store) GetMedia(ctx context.Context, mediaID string) (*Media, error) {
	m := &Media{ID: mediaID}
	err := s.db.QueryRowContext(ctx, `SELECT login_id, mime_type, file_name, size FROM media WHERE media_id=$1`, mediaID).
		Scan(&m.LoginID, &m.MimeType, &m.FileName, &m.Size)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return m, err
}

// DeleteLogin removes everything the host stored for a login except its seq counter,
// so a later re-login of the same remote account never reuses a seq.
func (s *Store) DeleteLogin(ctx context.Context, loginID string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.QueryContext(ctx, `SELECT media_id FROM media WHERE login_id=$1`, loginID)
	if err != nil {
		return nil, err
	}
	var mediaIDs []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		mediaIDs = append(mediaIDs, id)
	}
	rows.Close()
	for _, table := range []string{"outbox", "conversations", "messages", "sends", "media"} {
		if _, err = s.db.ExecContext(ctx, `DELETE FROM `+table+` WHERE login_id=$1`, loginID); err != nil {
			return nil, err
		}
	}
	return mediaIDs, nil
}
