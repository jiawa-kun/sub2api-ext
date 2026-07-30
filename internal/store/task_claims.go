package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
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
CREATE INDEX IF NOT EXISTS idx_task_claims_user_period ON task_claims(user_id, period_key);
`)
	return err
}

func (s *Store) GetTaskClaim(ctx context.Context, userID int64, taskID, periodKey string) (*TaskClaim, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, user_id, task_id, period_key, amount, ledger_id, created_at
FROM task_claims WHERE user_id=? AND task_id=? AND period_key=?
`, userID, taskID, periodKey)
	return scanTaskClaim(row)
}

// ListTaskClaimsByPeriods returns claims for a user limited to the given period keys.
// Keyed as taskID + "\x1f" + periodKey for O(1) lookup by callers.
func (s *Store) ListTaskClaimsByPeriods(ctx context.Context, userID int64, periodKeys []string) (map[string]TaskClaim, error) {
	out := make(map[string]TaskClaim)
	if userID <= 0 || len(periodKeys) == 0 {
		return out, nil
	}
	// de-dup period keys
	uniq := make([]string, 0, len(periodKeys))
	seen := map[string]struct{}{}
	for _, k := range periodKeys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		uniq = append(uniq, k)
	}
	if len(uniq) == 0 {
		return out, nil
	}

	var b strings.Builder
	args := make([]any, 0, 1+len(uniq))
	b.WriteString(`SELECT id, user_id, task_id, period_key, amount, ledger_id, created_at
FROM task_claims WHERE user_id=? AND period_key IN (`)
	args = append(args, userID)
	for i, k := range uniq {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('?')
		args = append(args, k)
	}
	b.WriteByte(')')

	rows, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		c, err := scanTaskClaimRows(rows)
		if err != nil {
			return nil, err
		}
		out[TaskClaimKey(c.TaskID, c.PeriodKey)] = *c
	}
	return out, rows.Err()
}

// TaskClaimKey builds the map key used by ListTaskClaimsByPeriods.
func TaskClaimKey(taskID, periodKey string) string {
	return taskID + "\x1f" + periodKey
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

func scanTaskClaim(row *sql.Row) (*TaskClaim, error) {
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

func scanTaskClaimRows(rows *sql.Rows) (*TaskClaim, error) {
	var c TaskClaim
	var created string
	if err := rows.Scan(&c.ID, &c.UserID, &c.TaskID, &c.PeriodKey, &c.Amount, &c.LedgerID, &created); err != nil {
		return nil, err
	}
	if t, e := time.Parse(time.RFC3339Nano, created); e == nil {
		c.CreatedAt = t
	}
	return &c, nil
}
