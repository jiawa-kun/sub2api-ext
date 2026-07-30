package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// TaskClaim records one claimed task reward for a period.
type TaskClaim struct {
	ID        int64
	UserID    int64
	TaskID    string
	PeriodKey string
	Amount    float64
	LedgerID  int64
	CreatedAt time.Time
}

func (s *Store) ensureTaskSchema() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS task_claims (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  task_id TEXT NOT NULL,
  period_key TEXT NOT NULL,
  amount REAL NOT NULL DEFAULT 0,
  ledger_id INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  UNIQUE(user_id, task_id, period_key)
);
CREATE INDEX IF NOT EXISTS idx_task_claims_user ON task_claims(user_id, created_at DESC);
`)
	return err
}

func (s *Store) GetTaskClaim(ctx context.Context, userID int64, taskID, periodKey string) (*TaskClaim, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, user_id, task_id, period_key, amount, ledger_id, created_at
FROM task_claims WHERE user_id=? AND task_id=? AND period_key=?
`, userID, taskID, periodKey)
	var c TaskClaim
	var created string
	err := row.Scan(&c.ID, &c.UserID, &c.TaskID, &c.PeriodKey, &c.Amount, &c.LedgerID, &created)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if t, e := time.Parse(time.RFC3339Nano, created); e == nil {
		c.CreatedAt = t
	}
	return &c, nil
}

func (s *Store) InsertTaskClaim(ctx context.Context, c TaskClaim) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx, `
INSERT INTO task_claims(user_id, task_id, period_key, amount, ledger_id, created_at)
VALUES(?,?,?,?,?,?)
`, c.UserID, c.TaskID, c.PeriodKey, c.Amount, c.LedgerID, now)
	if err != nil {
		if isUniqueViolation(err) {
			return 0, fmt.Errorf("already claimed")
		}
		return 0, err
	}
	return res.LastInsertId()
}

// CountCheckinsInRange counts distinct checkin days for user in [from,to].
func (s *Store) CountCheckinsInRange(ctx context.Context, userID int64, from, to string) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `
SELECT COUNT(1) FROM checkin_records WHERE user_id=? AND checkin_date>=? AND checkin_date<=?
`, userID, from, to).Scan(&n)
	return n, err
}

// CountLotteryInRange counts lottery draws in range.
func (s *Store) CountLotteryInRange(ctx context.Context, userID int64, from, to string) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `
SELECT COUNT(1) FROM lottery_draws WHERE user_id=? AND draw_date>=? AND draw_date<=?
`, userID, from, to).Scan(&n)
	return n, err
}
