package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var ErrAlreadyCheckedIn = errors.New("already checked in today")

type Record struct {
	ID          int64
	UserID      int64
	CheckinDate string
	Amount      float64
	NewBalance  float64
	CreatedAt   time.Time
}

type SettingsAudit struct {
	ID        int64
	ActorType string
	ActorID   int64
	ActorName string
	Source    string
	OldJSON   string
	NewJSON   string
	ClientIP  string
	UserAgent string
	CreatedAt time.Time
}

type DayStats struct {
	Date        string
	Count       int64
	TotalAmount float64
}

type Store struct {
	db *sql.DB
}

func Open(sqlitePath string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(sqlitePath), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir data: %w", err)
	}
	db, err := sql.Open("sqlite", sqlitePath+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS checkin_records (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  checkin_date TEXT NOT NULL,
  amount REAL NOT NULL,
  new_balance REAL NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_checkin_user_date
  ON checkin_records(user_id, checkin_date);
CREATE INDEX IF NOT EXISTS idx_checkin_user_created
  ON checkin_records(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_checkin_date
  ON checkin_records(checkin_date);
CREATE INDEX IF NOT EXISTS idx_checkin_date_user
  ON checkin_records(checkin_date, user_id);

CREATE TABLE IF NOT EXISTS app_settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS settings_audit (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  actor_type TEXT NOT NULL,
  actor_id INTEGER NOT NULL DEFAULT 0,
  actor_name TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL,
  old_json TEXT NOT NULL,
  new_json TEXT NOT NULL,
  client_ip TEXT NOT NULL DEFAULT '',
  user_agent TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_settings_audit_created
  ON settings_audit(created_at DESC);
`)
	if err != nil {
		return err
	}
	// best-effort column upgrades for older DBs
	_, _ = s.db.Exec(`ALTER TABLE settings_audit ADD COLUMN client_ip TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE settings_audit ADD COLUMN user_agent TEXT NOT NULL DEFAULT ''`)

	_, err = s.db.Exec(`
CREATE TABLE IF NOT EXISTS patrol_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  trigger_type TEXT NOT NULL,
  status TEXT NOT NULL,
  started_at TEXT NOT NULL,
  finished_at TEXT NOT NULL DEFAULT '',
  stats_json TEXT NOT NULL DEFAULT '{}',
  error TEXT NOT NULL DEFAULT '',
  log_json TEXT NOT NULL DEFAULT '[]'
);
CREATE INDEX IF NOT EXISTS idx_patrol_runs_started
  ON patrol_runs(started_at DESC);

CREATE TABLE IF NOT EXISTS patrol_account_state (
  account_id INTEGER PRIMARY KEY,
  account_name TEXT NOT NULL DEFAULT '',
  group_key TEXT NOT NULL DEFAULT '',
  consecutive_fail INTEGER NOT NULL DEFAULT 0,
  last_status TEXT NOT NULL DEFAULT '',
  last_reason TEXT NOT NULL DEFAULT '',
  last_action TEXT NOT NULL DEFAULT '',
  last_ok_at TEXT NOT NULL DEFAULT '',
  last_fail_at TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_patrol_account_state_fail
  ON patrol_account_state(consecutive_fail DESC);
`)
	if err != nil {
		return err
	}

	// The UNIQUE(user_id, draw_date) index is what actually enforces
	// "one draw per user per day"; the handler check is only a fast path.
	_, err = s.db.Exec(`
CREATE TABLE IF NOT EXISTS lottery_draws (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  draw_date TEXT NOT NULL,
  prize_label TEXT NOT NULL DEFAULT '',
  prize_type TEXT NOT NULL DEFAULT 'none',
  amount REAL NOT NULL DEFAULT 0,
  new_balance REAL NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_lottery_user_date
  ON lottery_draws(user_id, draw_date);
CREATE INDEX IF NOT EXISTS idx_lottery_date
  ON lottery_draws(draw_date);
CREATE INDEX IF NOT EXISTS idx_lottery_date_user
  ON lottery_draws(draw_date, user_id);
CREATE INDEX IF NOT EXISTS idx_lottery_created
  ON lottery_draws(created_at DESC);
`)
	if err != nil {
		return err
	}
	if err := s.ensureLedgerSchema(); err != nil {
		return err
	}
	if err := s.ensureCampaignSchema(); err != nil {
		return err
	}
	if err := s.ensureTaskSchema(); err != nil {
		return err
	}
	return nil
}

func (s *Store) GetSetting(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `
INSERT INTO app_settings(key, value, updated_at) VALUES(?, ?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
`, key, value, now)
	return err
}


func (s *Store) DeleteSetting(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM app_settings WHERE key = ?`, key)
	return err
}

func (s *Store) SetSettings(ctx context.Context, kv map[string]string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for k, v := range kv {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO app_settings(key, value, updated_at) VALUES(?, ?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
`, k, v, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) InsertSettingsAudit(ctx context.Context, a SettingsAudit) (int64, error) {
	now := a.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	res, err := s.db.ExecContext(ctx, `
INSERT INTO settings_audit (actor_type, actor_id, actor_name, source, old_json, new_json, client_ip, user_agent, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
`, a.ActorType, a.ActorID, a.ActorName, a.Source, a.OldJSON, a.NewJSON, a.ClientIP, a.UserAgent, now.Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) GetSettingsAudit(ctx context.Context, id int64) (*SettingsAudit, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, actor_type, actor_id, actor_name, source, old_json, new_json,
       COALESCE(client_ip,''), COALESCE(user_agent,''), created_at
FROM settings_audit WHERE id = ?
`, id)
	var a SettingsAudit
	var created string
	if err := row.Scan(&a.ID, &a.ActorType, &a.ActorID, &a.ActorName, &a.Source, &a.OldJSON, &a.NewJSON, &a.ClientIP, &a.UserAgent, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if t, err := time.Parse(time.RFC3339Nano, created); err == nil {
		a.CreatedAt = t
	} else if t, err := time.Parse(time.RFC3339, created); err == nil {
		a.CreatedAt = t
	}
	return &a, nil
}

func (s *Store) ListSettingsAudit(ctx context.Context, limit int) ([]SettingsAudit, error) {
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, actor_type, actor_id, actor_name, source, old_json, new_json,
       COALESCE(client_ip,''), COALESCE(user_agent,''), created_at
FROM settings_audit
ORDER BY id DESC
LIMIT ?
`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SettingsAudit
	for rows.Next() {
		var a SettingsAudit
		var created string
		if err := rows.Scan(&a.ID, &a.ActorType, &a.ActorID, &a.ActorName, &a.Source, &a.OldJSON, &a.NewJSON, &a.ClientIP, &a.UserAgent, &created); err != nil {
			return nil, err
		}
		if t, err := time.Parse(time.RFC3339Nano, created); err == nil {
			a.CreatedAt = t
		} else if t, err := time.Parse(time.RFC3339, created); err == nil {
			a.CreatedAt = t
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) SumAmountByDate(ctx context.Context, date string) (float64, error) {
	var sum sql.NullFloat64
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(amount), 0) FROM checkin_records WHERE checkin_date = ?`, date,
	).Scan(&sum)
	if err != nil {
		return 0, err
	}
	if sum.Valid {
		return sum.Float64, nil
	}
	return 0, nil
}

func (s *Store) StatsByDate(ctx context.Context, date string) (DayStats, error) {
	var st DayStats
	st.Date = date
	err := s.db.QueryRowContext(ctx, `
SELECT COUNT(1), COALESCE(SUM(amount), 0)
FROM checkin_records WHERE checkin_date = ?
`, date).Scan(&st.Count, &st.TotalAmount)
	return st, err
}

func (s *Store) StatsRecentDays(ctx context.Context, fromDate, toDate string) ([]DayStats, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT checkin_date, COUNT(1), COALESCE(SUM(amount), 0)
FROM checkin_records
WHERE checkin_date >= ? AND checkin_date <= ?
GROUP BY checkin_date
ORDER BY checkin_date ASC
`, fromDate, toDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DayStats
	for rows.Next() {
		var st DayStats
		if err := rows.Scan(&st.Date, &st.Count, &st.TotalAmount); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s *Store) ListByDate(ctx context.Context, date string, limit int) ([]Record, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, user_id, checkin_date, amount, new_balance, created_at
FROM checkin_records WHERE checkin_date = ?
ORDER BY id DESC LIMIT ?
`, date, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var r Record
		var created string
		if err := rows.Scan(&r.ID, &r.UserID, &r.CheckinDate, &r.Amount, &r.NewBalance, &created); err != nil {
			return nil, err
		}
		if t, err := time.Parse(time.RFC3339Nano, created); err == nil {
			r.CreatedAt = t
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) HasCheckedIn(ctx context.Context, userID int64, date string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM checkin_records WHERE user_id = ? AND checkin_date = ?`,
		userID, date,
	).Scan(&n)
	return n > 0, err
}

func (s *Store) CountByUser(ctx context.Context, userID int64) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM checkin_records WHERE user_id = ?`, userID,
	).Scan(&n)
	return n, err
}

func (s *Store) TryInsert(ctx context.Context, userID int64, date string, amount, newBalance float64) (*Record, error) {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `
INSERT INTO checkin_records (user_id, checkin_date, amount, new_balance, created_at)
VALUES (?, ?, ?, ?, ?)
`, userID, date, amount, newBalance, now.Format(time.RFC3339Nano))
	if err == nil {
		InvalidateRankingCache()
	}
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrAlreadyCheckedIn
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &Record{
		ID:          id,
		UserID:      userID,
		CheckinDate: date,
		Amount:      amount,
		NewBalance:  newBalance,
		CreatedAt:   now,
	}, nil
}

func (s *Store) Get(ctx context.Context, userID int64, date string) (*Record, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, user_id, checkin_date, amount, new_balance, created_at
FROM checkin_records WHERE user_id = ? AND checkin_date = ?
`, userID, date)
	var r Record
	var created string
	if err := row.Scan(&r.ID, &r.UserID, &r.CheckinDate, &r.Amount, &r.NewBalance, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if t, err := time.Parse(time.RFC3339Nano, created); err == nil {
		r.CreatedAt = t
	}
	return &r, nil
}

func (s *Store) ListRecent(ctx context.Context, userID int64, limit int) ([]Record, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, user_id, checkin_date, amount, new_balance, created_at
FROM checkin_records WHERE user_id = ?
ORDER BY checkin_date DESC LIMIT ?
`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var r Record
		var created string
		if err := rows.Scan(&r.ID, &r.UserID, &r.CheckinDate, &r.Amount, &r.NewBalance, &created); err != nil {
			return nil, err
		}
		if t, err := time.Parse(time.RFC3339Nano, created); err == nil {
			r.CreatedAt = t
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) Delete(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM checkin_records WHERE id = ?`, id)
	return err
}

func (s *Store) UpdateNewBalance(ctx context.Context, id int64, newBalance float64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE checkin_records SET new_balance = ? WHERE id = ?`, newBalance, id)
	return err
}

// CountStreakBefore returns consecutive checked-in days ending at yesterday (relative to today).
// Implemented with one range query instead of up to 366 point lookups.
func (s *Store) CountStreakBefore(ctx context.Context, userID int64, today string) (int, error) {
	d, err := time.Parse("2006-01-02", today)
	if err != nil {
		return 0, err
	}
	// 400-day lookback is enough for any practical streak UI/reward path.
	from := d.AddDate(0, 0, -400).Format("2006-01-02")
	toYesterday := d.AddDate(0, 0, -1).Format("2006-01-02")
	rows, err := s.db.QueryContext(ctx, `SELECT checkin_date FROM checkin_records
WHERE user_id = ? AND checkin_date >= ? AND checkin_date <= ?`, userID, from, toYesterday)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	have := make(map[string]struct{}, 64)
	for rows.Next() {
		var day string
		if err := rows.Scan(&day); err != nil {
			return 0, err
		}
		have[day] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	streak := 0
	for i := 1; i <= 400; i++ {
		day := d.AddDate(0, 0, -i).Format("2006-01-02")
		if _, ok := have[day]; !ok {
			break
		}
		streak++
	}
	return streak, nil
}

// PingDB ensures sqlite is usable.
func (s *Store) PingDB(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// ListByUserMonth returns check-ins for a user within [fromDate, toDate] inclusive.
func (s *Store) ListByUserMonth(ctx context.Context, userID int64, fromDate, toDate string) ([]Record, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, user_id, checkin_date, amount, new_balance, created_at
FROM checkin_records
WHERE user_id = ? AND checkin_date >= ? AND checkin_date <= ?
ORDER BY checkin_date ASC
`, userID, fromDate, toDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var r Record
		var created string
		if err := rows.Scan(&r.ID, &r.UserID, &r.CheckinDate, &r.Amount, &r.NewBalance, &created); err != nil {
			return nil, err
		}
		if t, err := time.Parse(time.RFC3339Nano, created); err == nil {
			r.CreatedAt = t
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") || strings.Contains(msg, "unique constraint")
}

