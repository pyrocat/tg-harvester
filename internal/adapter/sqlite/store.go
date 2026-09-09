package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	sqlite "modernc.org/sqlite"

	"tg-harvester/internal/domain"
)

// Store implements the persistence ports of the app package on top of SQLite.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// New creates a Store over an opened, migrated database.
func New(db *sql.DB) *Store {
	return &Store{db: db, now: time.Now}
}

// Settings returns the runtime settings.
func (s *Store) Settings(ctx context.Context) (domain.Settings, error) {
	var (
		st       domain.Settings
		poll     int64
		settle   int64
		hdrName  string
		hdrValue string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT endpoint_url, endpoint_header_name, endpoint_header_value, poll_interval_sec, settle_delay_sec
		FROM settings WHERE id = 1`).Scan(&st.Endpoint.URL, &hdrName, &hdrValue, &poll, &settle)
	if err != nil {
		return domain.Settings{}, fmt.Errorf("select settings: %w", err)
	}
	st.Endpoint.HeaderName = hdrName
	st.Endpoint.HeaderValue = hdrValue
	st.PollInterval = time.Duration(poll) * time.Second
	st.SettleDelay = time.Duration(settle) * time.Second
	return st, nil
}

// SaveSettings stores the runtime settings.
func (s *Store) SaveSettings(ctx context.Context, st domain.Settings) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE settings SET endpoint_url = ?, endpoint_header_name = ?, endpoint_header_value = ?,
			poll_interval_sec = ?, settle_delay_sec = ?, updated_at = ?
		WHERE id = 1`,
		st.Endpoint.URL, st.Endpoint.HeaderName, st.Endpoint.HeaderValue,
		int64(st.PollInterval/time.Second), int64(st.SettleDelay/time.Second), s.timestamp())
	if err != nil {
		return fmt.Errorf("update settings: %w", err)
	}
	return nil
}

// Credentials returns the Telegram credentials; zero values when unset.
func (s *Store) Credentials(ctx context.Context) (domain.Credentials, error) {
	var c domain.Credentials
	err := s.db.QueryRowContext(ctx, `SELECT api_id, api_hash, phone FROM settings WHERE id = 1`).
		Scan(&c.APIID, &c.APIHash, &c.Phone)
	if err != nil {
		return domain.Credentials{}, fmt.Errorf("select credentials: %w", err)
	}
	return c, nil
}

// SaveCredentials stores the Telegram credentials.
func (s *Store) SaveCredentials(ctx context.Context, c domain.Credentials) error {
	_, err := s.db.ExecContext(ctx, `UPDATE settings SET api_id = ?, api_hash = ?, phone = ?, updated_at = ? WHERE id = 1`,
		c.APIID, c.APIHash, c.Phone, s.timestamp())
	if err != nil {
		return fmt.Errorf("update credentials: %w", err)
	}
	return nil
}

// LoadSession returns the stored Telegram session bytes.
func (s *Store) LoadSession(ctx context.Context) ([]byte, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT data FROM telegram_session WHERE id = 1`).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("select session: %w", err)
	}
	return data, nil
}

// StoreSession upserts the Telegram session bytes.
func (s *Store) StoreSession(ctx context.Context, data []byte) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO telegram_session (id, data, updated_at) VALUES (1, ?, ?)
		ON CONFLICT (id) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
		data, s.timestamp())
	if err != nil {
		return fmt.Errorf("upsert session: %w", err)
	}
	return nil
}

// DeleteSession forgets the Telegram session.
func (s *Store) DeleteSession(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM telegram_session`); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

const channelColumns = `id, access_hash, username, title, cursor, enabled, last_polled_at, last_error, added_at`

// List returns all channels in insertion order.
func (s *Store) List(ctx context.Context) ([]domain.Channel, error) {
	return s.queryChannels(ctx, `SELECT `+channelColumns+` FROM channels ORDER BY added_at, id`)
}

// ListEnabled returns the channels that should be harvested.
func (s *Store) ListEnabled(ctx context.Context) ([]domain.Channel, error) {
	return s.queryChannels(ctx, `SELECT `+channelColumns+` FROM channels WHERE enabled = 1 ORDER BY added_at, id`)
}

// Get returns one channel.
func (s *Store) Get(ctx context.Context, id domain.ChannelID) (domain.Channel, error) {
	chs, err := s.queryChannels(ctx, `SELECT `+channelColumns+` FROM channels WHERE id = ?`, int64(id))
	if err != nil {
		return domain.Channel{}, err
	}
	if len(chs) == 0 {
		return domain.Channel{}, domain.ErrNotFound
	}
	return chs[0], nil
}

// Add inserts a channel; domain.ErrAlreadyExists when the ID is taken.
func (s *Store) Add(ctx context.Context, ch domain.Channel) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO channels (`+channelColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		int64(ch.ID), ch.AccessHash, ch.Username, ch.Title, ch.Cursor, boolInt(ch.Enabled),
		nullTime(ch.LastPolledAt), ch.LastError, formatTime(ch.AddedAt))
	if err != nil {
		if isConstraintError(err) {
			return domain.ErrAlreadyExists
		}
		return fmt.Errorf("insert channel: %w", err)
	}
	return nil
}

// Remove deletes a channel.
func (s *Store) Remove(ctx context.Context, id domain.ChannelID) error {
	return s.updateOne(ctx, `DELETE FROM channels WHERE id = ?`, int64(id))
}

// SetEnabled toggles harvesting of a channel.
func (s *Store) SetEnabled(ctx context.Context, id domain.ChannelID, enabled bool) error {
	return s.updateOne(ctx, `UPDATE channels SET enabled = ? WHERE id = ?`, boolInt(enabled), int64(id))
}

// ResetCursor moves the cursor and clears the last error.
func (s *Store) ResetCursor(ctx context.Context, id domain.ChannelID, cursor int64) error {
	return s.updateOne(ctx, `UPDATE channels SET cursor = ?, last_error = '' WHERE id = ?`, cursor, int64(id))
}

// AdvanceCursor records a successful delivery.
func (s *Store) AdvanceCursor(ctx context.Context, id domain.ChannelID, cursor int64, at time.Time) error {
	return s.updateOne(ctx, `UPDATE channels SET cursor = ?, last_polled_at = ? WHERE id = ?`, cursor, formatTime(at), int64(id))
}

// RecordPoll stores the outcome of a poll.
func (s *Store) RecordPoll(ctx context.Context, id domain.ChannelID, at time.Time, errMsg string) error {
	return s.updateOne(ctx, `UPDATE channels SET last_polled_at = ?, last_error = ? WHERE id = ?`, formatTime(at), errMsg, int64(id))
}

func (s *Store) updateOne(ctx context.Context, query string, args ...any) error {
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (s *Store) queryChannels(ctx context.Context, query string, args ...any) ([]domain.Channel, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("select channels: %w", err)
	}
	defer rows.Close()
	var out []domain.Channel
	for rows.Next() {
		var (
			ch       domain.Channel
			id       int64
			enabled  int64
			polledAt sql.NullString
			addedAt  string
		)
		if err := rows.Scan(&id, &ch.AccessHash, &ch.Username, &ch.Title, &ch.Cursor, &enabled, &polledAt, &ch.LastError, &addedAt); err != nil {
			return nil, fmt.Errorf("scan channel: %w", err)
		}
		ch.ID = domain.ChannelID(id)
		ch.Enabled = enabled != 0
		ch.LastPolledAt = parseTime(polledAt.String)
		ch.AddedAt = parseTime(addedAt)
		out = append(out, ch)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate channels: %w", err)
	}
	return out, nil
}

func (s *Store) timestamp() string {
	return formatTime(s.now())
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return formatTime(t)
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func isConstraintError(err error) bool {
	var se *sqlite.Error
	if errors.As(err, &se) {
		// SQLITE_CONSTRAINT_PRIMARYKEY (1555) and SQLITE_CONSTRAINT_UNIQUE (2067).
		return se.Code() == 1555 || se.Code() == 2067
	}
	return strings.Contains(err.Error(), "constraint failed")
}
