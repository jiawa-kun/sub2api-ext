package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const (
	RedistributionBatchDraft         = "draft"
	RedistributionBatchRunning       = "running"
	RedistributionBatchSuccess       = "success"
	RedistributionBatchPartial       = "partial"
	RedistributionBatchFailed        = "failed"
	RedistributionBatchStopped       = "stopped"
	RedistributionBatchAwaitingClaim = "awaiting_claims"

	RedistributionRoleDonor     = "donor"
	RedistributionRoleRecipient = "recipient"

	RedistributionEntryPlanned    = "planned"
	RedistributionEntryProcessing = "processing"
	RedistributionEntrySuccess    = "success"
	RedistributionEntryFailed     = "failed"
	RedistributionEntryPending    = "pending"
	RedistributionEntryClaimed    = "claimed"
	RedistributionEntryExpired    = "expired"
	RedistributionEntrySkipped    = "skipped"
)

type RedistributionBatch struct {
	ID                int64     `json:"id"`
	TriggerType       string    `json:"trigger_type"`
	PeriodKey         string    `json:"period_key"`
	Status            string    `json:"status"`
	ConfigJSON        string    `json:"config_json,omitempty"`
	CandidateCount    int       `json:"candidate_count"`
	RecipientCount    int       `json:"recipient_count"`
	PlannedReclaim    float64   `json:"planned_reclaim"`
	ActualReclaim     float64   `json:"actual_reclaim"`
	CarryIn           float64   `json:"carry_in"`
	PlannedDistribute float64   `json:"planned_distribute"`
	ActualDistribute  float64   `json:"actual_distribute"`
	Error             string    `json:"error,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	StartedAt         time.Time `json:"started_at,omitempty"`
	FinishedAt        time.Time `json:"finished_at,omitempty"`
}

type RedistributionEntry struct {
	ID             int64      `json:"id"`
	BatchID        int64      `json:"batch_id"`
	UserID         int64      `json:"user_id"`
	Role           string     `json:"role"`
	DisplayName    string     `json:"display_name"`
	BalanceBefore  float64    `json:"balance_before"`
	BalanceAfter   float64    `json:"balance_after"`
	LastActiveAt   *time.Time `json:"last_active_at,omitempty"`
	LastUsedAt     *time.Time `json:"last_used_at,omitempty"`
	ExtensionAt    *time.Time `json:"extension_at,omitempty"`
	UsageAmount    float64    `json:"usage_amount"`
	PlannedAmount  float64    `json:"planned_amount"`
	ActualAmount   float64    `json:"actual_amount"`
	Status         string     `json:"status"`
	Reason         string     `json:"reason"`
	IdempotencyKey string     `json:"idempotency_key"`
	LedgerID       int64      `json:"ledger_id"`
	Error          string     `json:"error,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

func (s *Store) ensureRedistributionSchema() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS redistribution_batches (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  trigger_type TEXT NOT NULL DEFAULT 'manual',
  period_key TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  config_json TEXT NOT NULL DEFAULT '{}',
  candidate_count INTEGER NOT NULL DEFAULT 0,
  recipient_count INTEGER NOT NULL DEFAULT 0,
  planned_reclaim REAL NOT NULL DEFAULT 0,
  actual_reclaim REAL NOT NULL DEFAULT 0,
  carry_in REAL NOT NULL DEFAULT 0,
  planned_distribute REAL NOT NULL DEFAULT 0,
  actual_distribute REAL NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  started_at TEXT NOT NULL DEFAULT '',
  finished_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_redistribution_batches_created
  ON redistribution_batches(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_redistribution_batches_schedule
  ON redistribution_batches(trigger_type, period_key, status);

CREATE TABLE IF NOT EXISTS redistribution_entries (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  batch_id INTEGER NOT NULL REFERENCES redistribution_batches(id) ON DELETE CASCADE,
  user_id INTEGER NOT NULL,
  role TEXT NOT NULL,
  display_name TEXT NOT NULL DEFAULT '',
  balance_before REAL NOT NULL DEFAULT 0,
  balance_after REAL NOT NULL DEFAULT 0,
  last_active_at TEXT NOT NULL DEFAULT '',
  last_used_at TEXT NOT NULL DEFAULT '',
  extension_at TEXT NOT NULL DEFAULT '',
  usage_amount REAL NOT NULL DEFAULT 0,
  planned_amount REAL NOT NULL DEFAULT 0,
  actual_amount REAL NOT NULL DEFAULT 0,
  status TEXT NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  idempotency_key TEXT NOT NULL DEFAULT '',
  ledger_id INTEGER NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT '',
  expires_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(batch_id, user_id, role)
);
CREATE INDEX IF NOT EXISTS idx_redistribution_entries_batch
  ON redistribution_entries(batch_id, role, status);
CREATE INDEX IF NOT EXISTS idx_redistribution_entries_user
  ON redistribution_entries(user_id, role, status, created_at DESC);
`)
	return err
}

func (s *Store) LatestExtensionActivity(ctx context.Context) (map[int64]time.Time, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT user_id, MAX(activity_at) FROM (
  SELECT user_id, created_at AS activity_at FROM checkin_records
  UNION ALL
  SELECT user_id, created_at AS activity_at FROM lottery_draws
  UNION ALL
  SELECT user_id, created_at AS activity_at FROM task_claims
) GROUP BY user_id
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]time.Time{}
	for rows.Next() {
		var id int64
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			out[id] = t
		}
	}
	return out, rows.Err()
}

func (s *Store) CreateRedistributionBatch(ctx context.Context, batch RedistributionBatch, entries []RedistributionEntry) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if batch.CreatedAt.IsZero() {
		batch.CreatedAt = time.Now().UTC()
	}
	if batch.Status == "" {
		batch.Status = RedistributionBatchDraft
	}
	res, err := tx.ExecContext(ctx, `
INSERT INTO redistribution_batches(
  trigger_type, period_key, status, config_json, candidate_count, recipient_count,
  planned_reclaim, actual_reclaim, carry_in, planned_distribute, actual_distribute,
  error, created_at, started_at, finished_at
) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
`, batch.TriggerType, batch.PeriodKey, batch.Status, batch.ConfigJSON, batch.CandidateCount, batch.RecipientCount,
		batch.PlannedReclaim, batch.ActualReclaim, batch.CarryIn, batch.PlannedDistribute, batch.ActualDistribute,
		batch.Error, formatOptionalTime(batch.CreatedAt), formatOptionalTime(batch.StartedAt), formatOptionalTime(batch.FinishedAt))
	if err != nil {
		return 0, err
	}
	batchID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	for _, entry := range entries {
		if entry.CreatedAt.IsZero() {
			entry.CreatedAt = now
		}
		if entry.UpdatedAt.IsZero() {
			entry.UpdatedAt = entry.CreatedAt
		}
		_, err = tx.ExecContext(ctx, `
INSERT INTO redistribution_entries(
  batch_id, user_id, role, display_name, balance_before, balance_after,
  last_active_at, last_used_at, extension_at, usage_amount, planned_amount, actual_amount,
  status, reason, idempotency_key, ledger_id, error, expires_at, created_at, updated_at
) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
`, batchID, entry.UserID, entry.Role, entry.DisplayName, entry.BalanceBefore, entry.BalanceAfter,
			formatTimePtr(entry.LastActiveAt), formatTimePtr(entry.LastUsedAt), formatTimePtr(entry.ExtensionAt), entry.UsageAmount,
			entry.PlannedAmount, entry.ActualAmount, entry.Status, entry.Reason, entry.IdempotencyKey, entry.LedgerID,
			entry.Error, formatTimePtr(entry.ExpiresAt), formatOptionalTime(entry.CreatedAt), formatOptionalTime(entry.UpdatedAt))
		if err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return batchID, nil
}

func (s *Store) GetRedistributionBatch(ctx context.Context, id int64) (*RedistributionBatch, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, trigger_type, period_key, status, config_json, candidate_count, recipient_count,
       planned_reclaim, actual_reclaim, carry_in, planned_distribute, actual_distribute,
       error, created_at, started_at, finished_at
FROM redistribution_batches WHERE id=?`, id)
	return scanRedistributionBatch(row)
}

func (s *Store) ListRedistributionBatches(ctx context.Context, limit int) ([]RedistributionBatch, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, trigger_type, period_key, status, config_json, candidate_count, recipient_count,
       planned_reclaim, actual_reclaim, carry_in, planned_distribute, actual_distribute,
       error, created_at, started_at, finished_at
FROM redistribution_batches ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]RedistributionBatch, 0, limit)
	for rows.Next() {
		batch, err := scanRedistributionBatch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *batch)
	}
	return out, rows.Err()
}

func (s *Store) CancelRedistributionBatch(ctx context.Context, id int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE redistribution_batches SET status=?, error=?, finished_at=? WHERE id=? AND status=?`, RedistributionBatchStopped, "cancelled by admin", time.Now().UTC().Format(time.RFC3339Nano), id, RedistributionBatchDraft)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *Store) DeleteRedistributionBatch(ctx context.Context, id int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM redistribution_batches WHERE id=? AND status=?`, id, RedistributionBatchDraft)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *Store) ListRedistributionEntries(ctx context.Context, batchID int64, role string) ([]RedistributionEntry, error) {
	query := `SELECT id, batch_id, user_id, role, display_name, balance_before, balance_after,
       last_active_at, last_used_at, extension_at, usage_amount, planned_amount, actual_amount,
       status, reason, idempotency_key, ledger_id, error, expires_at, created_at, updated_at
FROM redistribution_entries WHERE batch_id=?`
	args := []any{batchID}
	if strings.TrimSpace(role) != "" {
		query += ` AND role=?`
		args = append(args, role)
	}
	query += ` ORDER BY role ASC, planned_amount DESC, user_id ASC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RedistributionEntry{}
	for rows.Next() {
		entry, err := scanRedistributionEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *entry)
	}
	return out, rows.Err()
}

func (s *Store) GetRedistributionEntry(ctx context.Context, batchID, userID int64, role string) (*RedistributionEntry, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, batch_id, user_id, role, display_name, balance_before, balance_after,
       last_active_at, last_used_at, extension_at, usage_amount, planned_amount, actual_amount,
       status, reason, idempotency_key, ledger_id, error, expires_at, created_at, updated_at
FROM redistribution_entries WHERE batch_id=? AND user_id=? AND role=?`, batchID, userID, role)
	return scanRedistributionEntry(row)
}

func (s *Store) MarkRedistributionBatchRunning(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE redistribution_batches SET status=?, started_at=?, error='' WHERE id=? AND status=?`,
		RedistributionBatchRunning, time.Now().UTC().Format(time.RFC3339Nano), id, RedistributionBatchDraft)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return fmt.Errorf("batch is not executable")
	}
	return nil
}

func (s *Store) UpdateRedistributionEntry(ctx context.Context, entry RedistributionEntry) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE redistribution_entries SET
  balance_before=?, balance_after=?, planned_amount=?, actual_amount=?, status=?, reason=?,
  idempotency_key=?, ledger_id=?, error=?, expires_at=?, updated_at=?
WHERE id=?`, entry.BalanceBefore, entry.BalanceAfter, entry.PlannedAmount, entry.ActualAmount, entry.Status,
		entry.Reason, entry.IdempotencyKey, entry.LedgerID, entry.Error, formatTimePtr(entry.ExpiresAt),
		time.Now().UTC().Format(time.RFC3339Nano), entry.ID)
	return err
}

func (s *Store) FinishRedistributionBatch(ctx context.Context, batch RedistributionBatch) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE redistribution_batches SET status=?, candidate_count=?, recipient_count=?, planned_reclaim=?,
  actual_reclaim=?, carry_in=?, planned_distribute=?, actual_distribute=?, error=?, finished_at=?
WHERE id=?`, batch.Status, batch.CandidateCount, batch.RecipientCount, batch.PlannedReclaim,
		batch.ActualReclaim, batch.CarryIn, batch.PlannedDistribute, batch.ActualDistribute,
		batch.Error, time.Now().UTC().Format(time.RFC3339Nano), batch.ID)
	return err
}

func (s *Store) AddRedistributionDistributed(ctx context.Context, batchID int64, amount float64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE redistribution_batches SET actual_distribute=actual_distribute+? WHERE id=?`, amount, batchID)
	return err
}

func (s *Store) RedistributionAvailablePool(ctx context.Context) (float64, error) {
	if err := s.ExpireRedistributionEntitlements(ctx, time.Now().UTC()); err != nil {
		return 0, err
	}
	var reclaimed, distributed, reserved float64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(actual_reclaim),0), COALESCE(SUM(actual_distribute),0) FROM redistribution_batches`).Scan(&reclaimed, &distributed); err != nil {
		return 0, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(planned_amount),0) FROM redistribution_entries WHERE role=? AND status IN (?,?)`,
		RedistributionRoleRecipient, RedistributionEntryPending, RedistributionEntryProcessing).Scan(&reserved); err != nil {
		return 0, err
	}
	available := reclaimed - distributed - reserved
	if available < 0 && available > -0.0001 {
		available = 0
	}
	return available, nil
}

func (s *Store) ExpireRedistributionEntitlements(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE redistribution_entries SET status=?, updated_at=?
WHERE role=? AND status=? AND expires_at!='' AND expires_at<?`, RedistributionEntryExpired,
		now.Format(time.RFC3339Nano), RedistributionRoleRecipient, RedistributionEntryPending, now.Format(time.RFC3339Nano))
	return err
}

func (s *Store) MarkRedistributionClaimProcessing(ctx context.Context, entryID int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE redistribution_entries SET status=?, updated_at=? WHERE id=? AND status=?`,
		RedistributionEntryProcessing, time.Now().UTC().Format(time.RFC3339Nano), entryID, RedistributionEntryPending)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return fmt.Errorf("reward is not claimable")
	}
	return nil
}

func (s *Store) CompleteRedistributionClaim(ctx context.Context, entry RedistributionEntry) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE redistribution_entries SET status=?, actual_amount=?, balance_after=?, ledger_id=?, error='', updated_at=? WHERE id=? AND status=?`,
		RedistributionEntryClaimed, entry.ActualAmount, entry.BalanceAfter, entry.LedgerID,
		time.Now().UTC().Format(time.RFC3339Nano), entry.ID, RedistributionEntryProcessing)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return fmt.Errorf("claim state changed")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE redistribution_batches SET actual_distribute=actual_distribute+? WHERE id=?`, entry.ActualAmount, entry.BatchID); err != nil {
		return err
	}
	if err := refreshRedistributionBatchClaimStatusTx(ctx, tx, entry.BatchID); err != nil {
		return err
	}
	return tx.Commit()
}

func refreshRedistributionBatchClaimStatusTx(ctx context.Context, tx *sql.Tx, batchID int64) error {
	var current string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM redistribution_batches WHERE id=?`, batchID).Scan(&current); err != nil {
		return err
	}
	if current != RedistributionBatchAwaitingClaim {
		return nil
	}
	var remaining int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM redistribution_entries WHERE batch_id=? AND role=? AND status IN (?,?)`,
		batchID, RedistributionRoleRecipient, RedistributionEntryPending, RedistributionEntryProcessing).Scan(&remaining); err != nil {
		return err
	}
	if remaining > 0 {
		return nil
	}
	var abnormal int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM redistribution_entries WHERE batch_id=? AND status IN (?,?,?)`,
		batchID, RedistributionEntryFailed, RedistributionEntryExpired, RedistributionEntrySkipped).Scan(&abnormal); err != nil {
		return err
	}
	next := RedistributionBatchSuccess
	if abnormal > 0 {
		next = RedistributionBatchPartial
	}
	_, err := tx.ExecContext(ctx, `UPDATE redistribution_batches SET status=? WHERE id=? AND status=?`,
		next, batchID, RedistributionBatchAwaitingClaim)
	return err
}

func (s *Store) ResetRedistributionClaim(ctx context.Context, entryID int64, errText string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE redistribution_entries SET status=?, error=?, updated_at=? WHERE id=? AND status=?`,
		RedistributionEntryPending, errText, time.Now().UTC().Format(time.RFC3339Nano), entryID, RedistributionEntryProcessing)
	return err
}

func (s *Store) ListRedistributionRewards(ctx context.Context, userID int64, limit int) ([]RedistributionEntry, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, batch_id, user_id, role, display_name, balance_before, balance_after,
       last_active_at, last_used_at, extension_at, usage_amount, planned_amount, actual_amount,
       status, reason, idempotency_key, ledger_id, error, expires_at, created_at, updated_at
FROM redistribution_entries WHERE user_id=? AND role=? ORDER BY id DESC LIMIT ?`, userID, RedistributionRoleRecipient, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RedistributionEntry{}
	for rows.Next() {
		entry, err := scanRedistributionEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *entry)
	}
	return out, rows.Err()
}

func (s *Store) HasScheduledRedistributionBatch(ctx context.Context, periodKey string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM redistribution_batches WHERE trigger_type='schedule' AND period_key=? AND status NOT IN ('failed','stopped') LIMIT 1`, periodKey).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) TrimRedistributionBatches(ctx context.Context, keep int) error {
	if keep <= 0 {
		keep = 50
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM redistribution_batches WHERE id NOT IN (SELECT id FROM redistribution_batches ORDER BY id DESC LIMIT ?)`, keep)
	return err
}

type redistributionScanner interface{ Scan(...any) error }

func scanRedistributionBatch(row redistributionScanner) (*RedistributionBatch, error) {
	var out RedistributionBatch
	var created, started, finished string
	err := row.Scan(&out.ID, &out.TriggerType, &out.PeriodKey, &out.Status, &out.ConfigJSON,
		&out.CandidateCount, &out.RecipientCount, &out.PlannedReclaim, &out.ActualReclaim,
		&out.CarryIn, &out.PlannedDistribute, &out.ActualDistribute, &out.Error,
		&created, &started, &finished)
	if err != nil {
		return nil, err
	}
	out.CreatedAt = parseOptionalTime(created)
	out.StartedAt = parseOptionalTime(started)
	out.FinishedAt = parseOptionalTime(finished)
	return &out, nil
}

func scanRedistributionEntry(row redistributionScanner) (*RedistributionEntry, error) {
	var out RedistributionEntry
	var lastActive, lastUsed, extension, expires, created, updated string
	err := row.Scan(&out.ID, &out.BatchID, &out.UserID, &out.Role, &out.DisplayName,
		&out.BalanceBefore, &out.BalanceAfter, &lastActive, &lastUsed, &extension,
		&out.UsageAmount, &out.PlannedAmount, &out.ActualAmount, &out.Status,
		&out.Reason, &out.IdempotencyKey, &out.LedgerID, &out.Error, &expires, &created, &updated)
	if err != nil {
		return nil, err
	}
	out.LastActiveAt = parseTimePtr(lastActive)
	out.LastUsedAt = parseTimePtr(lastUsed)
	out.ExtensionAt = parseTimePtr(extension)
	out.ExpiresAt = parseTimePtr(expires)
	out.CreatedAt = parseOptionalTime(created)
	out.UpdatedAt = parseOptionalTime(updated)
	return &out, nil
}

func formatOptionalTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func formatTimePtr(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseOptionalTime(raw string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, raw)
	return t
}

func parseTimePtr(raw string) *time.Time {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return nil
	}
	return &t
}
