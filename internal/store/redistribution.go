package store

import (
	"context"
	"database/sql"
	"fmt"
	"math"
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

const (
	PoolLotAvailable = "available"
	PoolLotPartial   = "partial"
	PoolLotConsumed  = "consumed"
	PoolLotRefunded  = "refunded"
	PoolLotFailed    = "refund_failed"

	PoolDrawProcessing = "processing"
	PoolDrawSuccess    = "success"
	PoolDrawFailed     = "failed"

	PoolRefundManual     = "manual"
	PoolRefundAuto       = "auto"
	PoolRefundProcessing = "processing"
	PoolRefundSuccess    = "success"
	PoolRefundFailed     = "failed"
)

type RedistributionPoolLot struct {
	ID              int64     `json:"id"`
	SourceBatchID   int64     `json:"source_batch_id"`
	SourceUserID    int64     `json:"source_user_id"`
	OriginalAmount  float64   `json:"original_amount"`
	ConsumedAmount  float64   `json:"consumed_amount"`
	RefundedAmount  float64   `json:"refunded_amount"`
	RemainingAmount float64   `json:"remaining_amount"`
	Status          string    `json:"status"`
	CreatedAt       time.Time `json:"created_at"`
	ExpiresAt       time.Time `json:"expires_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type RedistributionPoolDraw struct {
	ID             int64     `json:"id"`
	UserID         int64     `json:"user_id"`
	DrawDate       string    `json:"draw_date"`
	Mode           string    `json:"mode"`
	Amount         float64   `json:"amount"`
	Status         string    `json:"status"`
	IdempotencyKey string    `json:"idempotency_key"`
	LedgerID       int64     `json:"ledger_id"`
	Error          string    `json:"error,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type RedistributionPoolConsumption struct {
	ID        int64     `json:"id"`
	DrawID    int64     `json:"draw_id"`
	LotID     int64     `json:"lot_id"`
	Amount    float64   `json:"amount"`
	CreatedAt time.Time `json:"created_at"`
}

type RedistributionPoolRefund struct {
	ID             int64     `json:"id"`
	LotID          int64     `json:"lot_id"`
	SourceUserID   int64     `json:"source_user_id"`
	Amount         float64   `json:"amount"`
	Kind           string    `json:"kind"`
	Status         string    `json:"status"`
	IdempotencyKey string    `json:"idempotency_key"`
	LedgerID       int64     `json:"ledger_id"`
	Error          string    `json:"error,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
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

CREATE TABLE IF NOT EXISTS redistribution_pool_lots (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  source_batch_id INTEGER NOT NULL REFERENCES redistribution_batches(id) ON DELETE CASCADE,
  source_user_id INTEGER NOT NULL,
  original_amount REAL NOT NULL,
  consumed_amount REAL NOT NULL DEFAULT 0,
  refunded_amount REAL NOT NULL DEFAULT 0,
  status TEXT NOT NULL,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_redistribution_pool_lots_batch_user
  ON redistribution_pool_lots(source_batch_id, source_user_id);
CREATE INDEX IF NOT EXISTS idx_redistribution_pool_lots_available
  ON redistribution_pool_lots(status, expires_at, id);
CREATE INDEX IF NOT EXISTS idx_redistribution_pool_lots_source
  ON redistribution_pool_lots(source_user_id, status, expires_at);

CREATE TABLE IF NOT EXISTS redistribution_pool_draws (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  draw_date TEXT NOT NULL,
  mode TEXT NOT NULL,
  amount REAL NOT NULL DEFAULT 0,
  status TEXT NOT NULL,
  idempotency_key TEXT NOT NULL,
  ledger_id INTEGER NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(user_id, draw_date)
);
CREATE INDEX IF NOT EXISTS idx_redistribution_pool_draws_date
  ON redistribution_pool_draws(draw_date, status);

CREATE TABLE IF NOT EXISTS redistribution_pool_consumptions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  draw_id INTEGER NOT NULL REFERENCES redistribution_pool_draws(id) ON DELETE CASCADE,
  lot_id INTEGER NOT NULL DEFAULT 0,
  amount REAL NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_redistribution_pool_consumptions_draw
  ON redistribution_pool_consumptions(draw_id);

CREATE TABLE IF NOT EXISTS redistribution_pool_refunds (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  lot_id INTEGER NOT NULL REFERENCES redistribution_pool_lots(id) ON DELETE CASCADE,
  source_user_id INTEGER NOT NULL,
  amount REAL NOT NULL,
  kind TEXT NOT NULL,
  status TEXT NOT NULL,
  idempotency_key TEXT NOT NULL,
  ledger_id INTEGER NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(lot_id, kind)
);
CREATE INDEX IF NOT EXISTS idx_redistribution_pool_refunds_status
  ON redistribution_pool_refunds(status, updated_at);
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
	var reclaimed, distributed, lotOriginal, reserved, legacyReserved float64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(actual_reclaim),0), COALESCE(SUM(actual_distribute),0) FROM redistribution_batches`).Scan(&reclaimed, &distributed); err != nil {
		return 0, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(original_amount),0) FROM redistribution_pool_lots`).Scan(&lotOriginal); err != nil {
		return 0, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(planned_amount),0) FROM redistribution_entries WHERE role=? AND status IN (?,?)`,
		RedistributionRoleRecipient, RedistributionEntryPending, RedistributionEntryProcessing).Scan(&reserved); err != nil {
		return 0, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(c.amount),0) FROM redistribution_pool_consumptions c JOIN redistribution_pool_draws d ON d.id=c.draw_id WHERE c.lot_id=0 AND d.status IN (?,?)`, PoolDrawProcessing, PoolDrawSuccess).Scan(&legacyReserved); err != nil {
		return 0, err
	}
	var lotsRemaining float64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(original_amount-consumed_amount-refunded_amount),0) FROM redistribution_pool_lots`).Scan(&lotsRemaining); err != nil {
		return 0, err
	}
	legacy := reclaimed - distributed - lotOriginal - reserved - legacyReserved
	if legacy < 0 {
		legacy = 0
	}
	available := legacy + lotsRemaining
	if available < 0 && available > -0.0001 {
		available = 0
	}
	return available, nil
}

func (s *Store) CreateRedistributionPoolLot(ctx context.Context, lot RedistributionPoolLot) (int64, error) {
	if lot.CreatedAt.IsZero() {
		lot.CreatedAt = time.Now().UTC()
	}
	if lot.UpdatedAt.IsZero() {
		lot.UpdatedAt = lot.CreatedAt
	}
	if lot.Status == "" {
		lot.Status = PoolLotAvailable
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO redistribution_pool_lots(source_batch_id,source_user_id,original_amount,consumed_amount,refunded_amount,status,created_at,expires_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		lot.SourceBatchID, lot.SourceUserID, lot.OriginalAmount, lot.ConsumedAmount, lot.RefundedAmount, lot.Status,
		formatOptionalTime(lot.CreatedAt), formatOptionalTime(lot.ExpiresAt), formatOptionalTime(lot.UpdatedAt))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) ListRedistributionPoolLots(ctx context.Context, sourceUserID int64, includeEmpty bool, limit int) ([]RedistributionPoolLot, error) {
	if limit <= 0 {
		limit = 100
	}
	query := `SELECT id,source_batch_id,source_user_id,original_amount,consumed_amount,refunded_amount,status,created_at,expires_at,updated_at FROM redistribution_pool_lots WHERE 1=1`
	args := []any{}
	if sourceUserID > 0 {
		query += ` AND source_user_id=?`
		args = append(args, sourceUserID)
	}
	if !includeEmpty {
		query += ` AND original_amount-consumed_amount-refunded_amount>0`
	}
	query += ` ORDER BY expires_at ASC,id ASC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RedistributionPoolLot{}
	for rows.Next() {
		var l RedistributionPoolLot
		var created, expires, updated string
		if err := rows.Scan(&l.ID, &l.SourceBatchID, &l.SourceUserID, &l.OriginalAmount, &l.ConsumedAmount, &l.RefundedAmount, &l.Status, &created, &expires, &updated); err != nil {
			return nil, err
		}
		l.CreatedAt = parseOptionalTime(created)
		l.ExpiresAt = parseOptionalTime(expires)
		l.UpdatedAt = parseOptionalTime(updated)
		l.RemainingAmount = floorStoreMoney(l.OriginalAmount - l.ConsumedAmount - l.RefundedAmount)
		if l.RemainingAmount < 0 {
			l.RemainingAmount = 0
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) GetRedistributionPoolDraw(ctx context.Context, userID int64, drawDate string) (*RedistributionPoolDraw, error) {
	var d RedistributionPoolDraw
	var created, updated string
	err := s.db.QueryRowContext(ctx, `SELECT id,user_id,draw_date,mode,amount,status,idempotency_key,ledger_id,error,created_at,updated_at FROM redistribution_pool_draws WHERE user_id=? AND draw_date=?`, userID, drawDate).Scan(&d.ID, &d.UserID, &d.DrawDate, &d.Mode, &d.Amount, &d.Status, &d.IdempotencyKey, &d.LedgerID, &d.Error, &created, &updated)
	if err != nil {
		return nil, err
	}
	d.CreatedAt = parseOptionalTime(created)
	d.UpdatedAt = parseOptionalTime(updated)
	return &d, nil
}

func (s *Store) ListRedistributionPoolDrawUsers(ctx context.Context, drawDate string) (map[int64]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT user_id FROM redistribution_pool_draws WHERE draw_date=? AND status=?`, drawDate, PoolDrawSuccess)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

func (s *Store) ReserveRedistributionPoolDraw(ctx context.Context, draw RedistributionPoolDraw, now time.Time) (*RedistributionPoolDraw, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if draw.CreatedAt.IsZero() {
		draw.CreatedAt = now.UTC()
	}
	draw.UpdatedAt = draw.CreatedAt
	draw.Status = PoolDrawProcessing
	res, err := tx.ExecContext(ctx, `INSERT INTO redistribution_pool_draws(user_id,draw_date,mode,amount,status,idempotency_key,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, draw.UserID, draw.DrawDate, draw.Mode, draw.Amount, draw.Status, draw.IdempotencyKey, formatOptionalTime(draw.CreatedAt), formatOptionalTime(draw.UpdatedAt))
	if err != nil {
		return nil, err
	}
	draw.ID, err = res.LastInsertId()
	if err != nil {
		return nil, err
	}
	need := floorStoreMoney(draw.Amount)
	if need <= 0 {
		return nil, fmt.Errorf("抽取额度必须大于 0")
	}
	var legacy float64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(actual_reclaim),0)-COALESCE(SUM(actual_distribute),0)-COALESCE((SELECT SUM(original_amount) FROM redistribution_pool_lots),0)-COALESCE((SELECT SUM(planned_amount) FROM redistribution_entries WHERE role=? AND status IN (?,?)),0)-COALESCE((SELECT SUM(c.amount) FROM redistribution_pool_consumptions c JOIN redistribution_pool_draws d ON d.id=c.draw_id WHERE c.lot_id=0 AND d.status=?),0) FROM redistribution_batches`, RedistributionRoleRecipient, RedistributionEntryPending, RedistributionEntryProcessing, PoolDrawProcessing).Scan(&legacy); err != nil {
		return nil, err
	}
	if legacy < 0 {
		legacy = 0
	}
	remaining := need
	if legacy > 0 {
		take := legacy
		if take > remaining {
			take = remaining
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO redistribution_pool_consumptions(draw_id,lot_id,amount,created_at) VALUES(?,?,?,?)`, draw.ID, 0, take, formatOptionalTime(now)); err != nil {
			return nil, err
		}
		remaining = floorStoreMoney(remaining - take)
	}
	if remaining > 0 {
		rows, err := tx.QueryContext(ctx, `SELECT id,original_amount,consumed_amount,refunded_amount FROM redistribution_pool_lots WHERE expires_at>? AND original_amount-consumed_amount-refunded_amount>0 ORDER BY expires_at ASC,id ASC`, formatOptionalTime(now))
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var original, consumed, refunded float64
			if err := rows.Scan(&id, &original, &consumed, &refunded); err != nil {
				rows.Close()
				return nil, err
			}
			avail := floorStoreMoney(original - consumed - refunded)
			if avail <= 0 {
				continue
			}
			take := avail
			if take > remaining {
				take = remaining
			}
			if _, err = tx.ExecContext(ctx, `UPDATE redistribution_pool_lots SET consumed_amount=consumed_amount+?,status=?,updated_at=? WHERE id=?`, take, PoolLotPartial, formatOptionalTime(now), id); err != nil {
				rows.Close()
				return nil, err
			}
			if floorStoreMoney(original-consumed-refunded-take) <= 0 {
				_, err = tx.ExecContext(ctx, `UPDATE redistribution_pool_lots SET status=? WHERE id=?`, PoolLotConsumed, id)
				if err != nil {
					rows.Close()
					return nil, err
				}
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO redistribution_pool_consumptions(draw_id,lot_id,amount,created_at) VALUES(?,?,?,?)`, draw.ID, id, take, formatOptionalTime(now)); err != nil {
				rows.Close()
				return nil, err
			}
			remaining = floorStoreMoney(remaining - take)
			if remaining <= 0 {
				break
			}
		}
		rows.Close()
	}
	if remaining > 0 {
		return nil, fmt.Errorf("回流池余额不足")
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &draw, nil
}

func (s *Store) CompleteRedistributionPoolDraw(ctx context.Context, drawID int64, ledgerID int64, errText string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE redistribution_pool_draws SET status=?,ledger_id=?,error=?,updated_at=? WHERE id=? AND status=?`, PoolDrawSuccess, ledgerID, errText, formatOptionalTime(time.Now().UTC()), drawID, PoolDrawProcessing)
	return err
}

func (s *Store) ResetRedistributionPoolDraw(ctx context.Context, drawID int64, errText string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT lot_id,amount FROM redistribution_pool_consumptions WHERE draw_id=?`, drawID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var lotID int64
		var amount float64
		if err := rows.Scan(&lotID, &amount); err != nil {
			rows.Close()
			return err
		}
		if lotID > 0 {
			if _, err = tx.ExecContext(ctx, `UPDATE redistribution_pool_lots SET consumed_amount=MAX(0,consumed_amount-?),status=?,updated_at=? WHERE id=?`, amount, PoolLotAvailable, formatOptionalTime(time.Now().UTC()), lotID); err != nil {
				rows.Close()
				return err
			}
		}
	}
	rows.Close()
	if _, err = tx.ExecContext(ctx, `DELETE FROM redistribution_pool_draws WHERE id=? AND status=?`, drawID, PoolDrawProcessing); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListExpiredRedistributionPoolLots(ctx context.Context, now time.Time, limit int) ([]RedistributionPoolLot, error) {
	if limit <= 0 {
		limit = 100
	}
	return s.listPoolLotsWhere(ctx, `expires_at<=? AND original_amount-consumed_amount-refunded_amount>0`, []any{formatOptionalTime(now)}, limit)
}

func (s *Store) listPoolLotsWhere(ctx context.Context, condition string, args []any, limit int) ([]RedistributionPoolLot, error) {
	query := `SELECT id,source_batch_id,source_user_id,original_amount,consumed_amount,refunded_amount,status,created_at,expires_at,updated_at FROM redistribution_pool_lots WHERE ` + condition + ` ORDER BY expires_at ASC,id ASC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RedistributionPoolLot{}
	for rows.Next() {
		var l RedistributionPoolLot
		var created, expires, updated string
		if err := rows.Scan(&l.ID, &l.SourceBatchID, &l.SourceUserID, &l.OriginalAmount, &l.ConsumedAmount, &l.RefundedAmount, &l.Status, &created, &expires, &updated); err != nil {
			return nil, err
		}
		l.CreatedAt = parseOptionalTime(created)
		l.ExpiresAt = parseOptionalTime(expires)
		l.UpdatedAt = parseOptionalTime(updated)
		l.RemainingAmount = floorStoreMoney(l.OriginalAmount - l.ConsumedAmount - l.RefundedAmount)
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) ReserveRedistributionPoolRefund(ctx context.Context, lotID int64, kind string, now time.Time) (*RedistributionPoolRefund, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var lot RedistributionPoolLot
	var created, expires, updated string
	if err := tx.QueryRowContext(ctx, `SELECT id,source_batch_id,source_user_id,original_amount,consumed_amount,refunded_amount,status,created_at,expires_at,updated_at FROM redistribution_pool_lots WHERE id=?`, lotID).Scan(&lot.ID, &lot.SourceBatchID, &lot.SourceUserID, &lot.OriginalAmount, &lot.ConsumedAmount, &lot.RefundedAmount, &lot.Status, &created, &expires, &updated); err != nil {
		return nil, err
	}
	amount := floorStoreMoney(lot.OriginalAmount - lot.ConsumedAmount - lot.RefundedAmount)
	if amount <= 0 {
		return nil, fmt.Errorf("没有可追回额度")
	}
	var processing int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM redistribution_pool_refunds WHERE lot_id=? AND status=?`, lotID, PoolRefundProcessing).Scan(&processing); err != nil {
		return nil, err
	}
	if processing > 0 {
		return nil, fmt.Errorf("该额度正在退款处理中")
	}
	key := fmt.Sprintf("redistribution-pool-refund:%d:%s", lotID, kind)
	var refund RedistributionPoolRefund
	var existingCreated, existingUpdated string
	err = tx.QueryRowContext(ctx, `SELECT id,source_user_id,amount,kind,status,idempotency_key,ledger_id,error,created_at,updated_at FROM redistribution_pool_refunds WHERE lot_id=? AND kind=?`, lotID, kind).Scan(&refund.ID, &refund.SourceUserID, &refund.Amount, &refund.Kind, &refund.Status, &refund.IdempotencyKey, &refund.LedgerID, &refund.Error, &existingCreated, &existingUpdated)
	if err == nil {
		if refund.Status == PoolRefundSuccess {
			return &refund, nil
		}
		refund.Amount = amount
		refund.Status = PoolRefundProcessing
		refund.UpdatedAt = now
	} else if err == sql.ErrNoRows {
		refund.SourceUserID = lot.SourceUserID
		refund.Amount = amount
		refund.Kind = kind
		refund.Status = PoolRefundProcessing
		refund.IdempotencyKey = key
		refund.CreatedAt = now
		refund.UpdatedAt = now
		res, er := tx.ExecContext(ctx, `INSERT INTO redistribution_pool_refunds(lot_id,source_user_id,amount,kind,status,idempotency_key,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, lotID, refund.SourceUserID, refund.Amount, refund.Kind, refund.Status, refund.IdempotencyKey, formatOptionalTime(now), formatOptionalTime(now))
		if er != nil {
			return nil, er
		}
		refund.ID, er = res.LastInsertId()
		if er != nil {
			return nil, er
		}
	} else {
		return nil, err
	}
	if err == nil && refund.ID > 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE redistribution_pool_refunds SET amount=?,status=?,error='',updated_at=? WHERE id=?`, refund.Amount, PoolRefundProcessing, formatOptionalTime(now), refund.ID); err != nil {
			return nil, err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE redistribution_pool_lots SET refunded_amount=refunded_amount+?,status=?,updated_at=? WHERE id=?`, amount, PoolLotRefunded, formatOptionalTime(now), lotID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &refund, nil
}

func (s *Store) CompleteRedistributionPoolRefund(ctx context.Context, refundID, ledgerID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE redistribution_pool_refunds SET status=?,ledger_id=?,updated_at=? WHERE id=? AND status=?`, PoolRefundSuccess, ledgerID, formatOptionalTime(time.Now().UTC()), refundID, PoolRefundProcessing)
	return err
}
func (s *Store) ResetRedistributionPoolRefund(ctx context.Context, refundID int64, errText string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var lotID int64
	var amountF float64
	if err := tx.QueryRowContext(ctx, `SELECT lot_id,amount FROM redistribution_pool_refunds WHERE id=?`, refundID).Scan(&lotID, &amountF); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE redistribution_pool_lots SET refunded_amount=MAX(0,refunded_amount-?),status=?,updated_at=? WHERE id=?`, amountF, PoolLotAvailable, formatOptionalTime(time.Now().UTC()), lotID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE redistribution_pool_refunds SET status=?,error=?,updated_at=? WHERE id=?`, PoolRefundFailed, errText, formatOptionalTime(time.Now().UTC()), refundID); err != nil {
		return err
	}
	return tx.Commit()
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
	_, err := s.db.ExecContext(ctx, `DELETE FROM redistribution_batches
WHERE id NOT IN (SELECT id FROM redistribution_batches ORDER BY id DESC LIMIT ?)
  AND NOT EXISTS (SELECT 1 FROM redistribution_pool_lots WHERE source_batch_id=redistribution_batches.id)`, keep)
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

func floorStoreMoney(value float64) float64 {
	return math.Floor((value+1e-9)*10000) / 10000
}
