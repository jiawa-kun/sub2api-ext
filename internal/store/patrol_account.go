package store

import (
	"context"
	"strings"
	"time"
)

// PatrolAccountState tracks per-account patrol health across runs so that a
// single upstream hiccup does not immediately disable or delete an account.
type PatrolAccountState struct {
	AccountID       int64  `json:"account_id"`
	AccountName     string `json:"account_name"`
	GroupKey        string `json:"group_key"`
	ConsecutiveFail int    `json:"consecutive_fail"`
	LastStatus      string `json:"last_status"`
	LastReason      string `json:"last_reason"`
	LastAction      string `json:"last_action"`
	LastOKAt        string `json:"last_ok_at"`
	LastFailAt      string `json:"last_fail_at"`
	UpdatedAt       string `json:"updated_at"`
}

const patrolReasonMaxLen = 500

// UpsertPatrolAccountFail records one failed check and returns the new
// consecutive failure count for the account.
func (s *Store) UpsertPatrolAccountFail(ctx context.Context, accountID int64, name, group, reason string) (int, error) {
	if accountID == 0 {
		return 0, nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	reason = truncateReason(reason)
	_, err := s.db.ExecContext(ctx, `
INSERT INTO patrol_account_state
  (account_id, account_name, group_key, consecutive_fail, last_status, last_reason, last_action, last_ok_at, last_fail_at, updated_at)
VALUES (?, ?, ?, 1, 'fail', ?, '', '', ?, ?)
ON CONFLICT(account_id) DO UPDATE SET
  account_name = excluded.account_name,
  group_key = excluded.group_key,
  consecutive_fail = patrol_account_state.consecutive_fail + 1,
  last_status = 'fail',
  last_reason = excluded.last_reason,
  last_fail_at = excluded.last_fail_at,
  updated_at = excluded.updated_at
`, accountID, name, group, reason, now, now)
	if err != nil {
		return 0, err
	}
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT consecutive_fail FROM patrol_account_state WHERE account_id = ?`, accountID).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// ResetPatrolAccountOK clears the consecutive failure counter after a success.
func (s *Store) ResetPatrolAccountOK(ctx context.Context, accountID int64, name, group string) error {
	if accountID == 0 {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `
INSERT INTO patrol_account_state
  (account_id, account_name, group_key, consecutive_fail, last_status, last_reason, last_action, last_ok_at, last_fail_at, updated_at)
VALUES (?, ?, ?, 0, 'ok', '', '', ?, '', ?)
ON CONFLICT(account_id) DO UPDATE SET
  account_name = excluded.account_name,
  group_key = excluded.group_key,
  consecutive_fail = 0,
  last_status = 'ok',
  last_reason = '',
  last_action = '',
  last_ok_at = excluded.last_ok_at,
  updated_at = excluded.updated_at
`, accountID, name, group, now, now)
	return err
}

// MarkPatrolAccountAction records the action finally applied to an account
// (disable / delete / none / pending).
func (s *Store) MarkPatrolAccountAction(ctx context.Context, accountID int64, action string) error {
	if accountID == 0 {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `
UPDATE patrol_account_state
SET last_action = ?, updated_at = ?
WHERE account_id = ?
`, action, now, accountID)
	return err
}

// ListPatrolAccountStates returns account health rows, optionally only the
// accounts that currently have at least one consecutive failure.
func (s *Store) ListPatrolAccountStates(ctx context.Context, onlyProblem bool, limit int) ([]PatrolAccountState, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `
SELECT account_id, account_name, group_key, consecutive_fail, last_status, last_reason, last_action, last_ok_at, last_fail_at, updated_at
FROM patrol_account_state
`
	if onlyProblem {
		query += "WHERE consecutive_fail > 0\n"
	}
	query += "ORDER BY consecutive_fail DESC, updated_at DESC\nLIMIT ?"

	rows, err := s.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]PatrolAccountState, 0, limit)
	for rows.Next() {
		var it PatrolAccountState
		if err := rows.Scan(&it.AccountID, &it.AccountName, &it.GroupKey, &it.ConsecutiveFail,
			&it.LastStatus, &it.LastReason, &it.LastAction, &it.LastOKAt, &it.LastFailAt, &it.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// DeletePatrolAccountState removes tracking for an account (used after delete).
func (s *Store) DeletePatrolAccountState(ctx context.Context, accountID int64) error {
	if accountID == 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM patrol_account_state WHERE account_id = ?`, accountID)
	return err
}

func truncateReason(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= patrolReasonMaxLen {
		return s
	}
	return s[:patrolReasonMaxLen] + "..."
}
