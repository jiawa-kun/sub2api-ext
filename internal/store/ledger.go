package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Ledger source constants.
const (
	LedgerSourceCheckin                    = "checkin"
	LedgerSourceLottery                    = "lottery"
	LedgerSourceRankReward                 = "rank_reward"
	LedgerSourceTask                       = "task"
	LedgerSourceInactiveReclaim            = "inactive_reclaim"
	LedgerSourceRedistribution             = "active_redistribution"
	LedgerSourceRedistributionCompensation = "redistribution_compensation"
	LedgerSourceManual                     = "manual"
	LedgerSourceBackfill                   = "backfill"
)

// Ledger status constants.
const (
	LedgerStatusSuccess = "success"
	LedgerStatusFailed  = "failed"
	LedgerStatusSkipped = "skipped"
)

// LedgerEntry is one extension-side credit attempt.
type LedgerEntry struct {
	ID             int64
	UserID         int64
	Source         string
	SourceRef      string
	Amount         float64
	IdempotencyKey string
	Status         string
	Notes          string
	Error          string
	CreatedAt      time.Time
}

// LedgerFilter lists ledger rows.
type LedgerFilter struct {
	Source string
	Status string // success|failed|skipped, optional
	UserID int64
	From   string // YYYY-MM-DD inclusive, optional
	To     string
	Limit  int
	Offset int
}

// LedgerDayStat aggregates one day.
type LedgerDayStat struct {
	Date   string  `json:"date"`
	Source string  `json:"source"`
	Count  int64   `json:"count"`
	Amount float64 `json:"amount"`
}

// LedgerStatusSummary aggregates counts/amounts by status for a date window.

// ledgerCreatedBounds converts inclusive YYYY-MM-DD filters into [from, toExclusive)
// bounds that keep created_at index-friendly without substr().
func ledgerCreatedBounds(from, to string) (fromBound, toExclusive string) {
	from = strings.TrimSpace(from)
	to = strings.TrimSpace(to)
	if from != "" {
		fromBound = from // RFC3339 starts with YYYY-MM-DD, so >= date is correct
	}
	if to != "" {
		if t, err := time.Parse("2006-01-02", to); err == nil {
			toExclusive = t.AddDate(0, 0, 1).Format("2006-01-02")
		} else {
			toExclusive = to + "\x7f" // fallback: still upper-bound the day prefix
		}
	}
	return fromBound, toExclusive
}

type LedgerStatusSummary struct {
	SuccessCount  int64   `json:"success_count"`
	SuccessAmount float64 `json:"success_amount"`
	FailedCount   int64   `json:"failed_count"`
	SkippedCount  int64   `json:"skipped_count"`
}

func (s *Store) ensureLedgerSchema() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS credit_ledger (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  source TEXT NOT NULL,
  source_ref TEXT NOT NULL DEFAULT '',
  amount REAL NOT NULL DEFAULT 0,
  idempotency_key TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  notes TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
DROP INDEX IF EXISTS idx_ledger_idem;
CREATE UNIQUE INDEX IF NOT EXISTS idx_ledger_idem
  ON credit_ledger(idempotency_key)
  WHERE idempotency_key != '' AND status IN ('success','skipped');
CREATE INDEX IF NOT EXISTS idx_ledger_created
  ON credit_ledger(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_ledger_user
  ON credit_ledger(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_ledger_source_date
  ON credit_ledger(source, created_at);
CREATE INDEX IF NOT EXISTS idx_ledger_status_created
  ON credit_ledger(status, created_at);
`)
	return err
}

// InsertLedger writes one row. Empty idempotency_key skips unique constraint.
func (s *Store) InsertLedger(ctx context.Context, e LedgerEntry) (int64, error) {
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	if e.Status == "" {
		e.Status = LedgerStatusSuccess
	}
	res, err := s.db.ExecContext(ctx, `
INSERT INTO credit_ledger(user_id, source, source_ref, amount, idempotency_key, status, notes, error, created_at)
VALUES(?,?,?,?,?,?,?,?,?)
`, e.UserID, e.Source, e.SourceRef, e.Amount, e.IdempotencyKey, e.Status, e.Notes, e.Error, e.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		if isUniqueViolation(err) {
			return 0, fmt.Errorf("duplicate idempotency key")
		}
		return 0, err
	}
	return res.LastInsertId()
}

// HasLedgerIdem returns true if a success/skipped row already exists for key.
func (s *Store) HasLedgerIdem(ctx context.Context, key string) (bool, error) {
	if strings.TrimSpace(key) == "" {
		return false, nil
	}
	var one int
	err := s.db.QueryRowContext(ctx, `
SELECT 1 FROM credit_ledger
WHERE idempotency_key = ? AND status IN ('success','skipped')
LIMIT 1
`, key).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// GetLedgerByIdem returns the row for an idempotency key.
func (s *Store) GetLedgerByIdem(ctx context.Context, key string) (*LedgerEntry, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, user_id, source, source_ref, amount, idempotency_key, status, notes, error, created_at
FROM credit_ledger WHERE idempotency_key = ? ORDER BY id DESC LIMIT 1
`, key)
	return scanLedger(row)
}

// ListLedger returns newest first.
func (s *Store) ListLedger(ctx context.Context, f LedgerFilter) ([]LedgerEntry, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Limit > 5000 {
		f.Limit = 5000
	}
	var b strings.Builder
	args := make([]any, 0, 8)
	b.WriteString(`SELECT id, user_id, source, source_ref, amount, idempotency_key, status, notes, error, created_at FROM credit_ledger WHERE 1=1`)
	if f.Source != "" {
		b.WriteString(` AND source = ?`)
		args = append(args, f.Source)
	}
	if f.Status != "" {
		b.WriteString(` AND status = ?`)
		args = append(args, f.Status)
	}
	if f.UserID > 0 {
		b.WriteString(` AND user_id = ?`)
		args = append(args, f.UserID)
	}
	fromBound, toExclusive := ledgerCreatedBounds(f.From, f.To)
	if fromBound != "" {
		b.WriteString(` AND created_at >= ?`)
		args = append(args, fromBound)
	}
	if toExclusive != "" {
		b.WriteString(` AND created_at < ?`)
		args = append(args, toExclusive)
	}
	b.WriteString(` ORDER BY id DESC LIMIT ? OFFSET ?`)
	args = append(args, f.Limit, f.Offset)

	rows, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LedgerEntry
	for rows.Next() {
		e, err := scanLedgerRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// CountLedger counts rows matching filter (ignores Limit/Offset).
func (s *Store) CountLedger(ctx context.Context, f LedgerFilter) (int64, error) {
	var b strings.Builder
	args := make([]any, 0, 6)
	b.WriteString(`SELECT COUNT(1) FROM credit_ledger WHERE 1=1`)
	if f.Source != "" {
		b.WriteString(` AND source = ?`)
		args = append(args, f.Source)
	}
	if f.Status != "" {
		b.WriteString(` AND status = ?`)
		args = append(args, f.Status)
	}
	if f.UserID > 0 {
		b.WriteString(` AND user_id = ?`)
		args = append(args, f.UserID)
	}
	fromBound, toExclusive := ledgerCreatedBounds(f.From, f.To)
	if fromBound != "" {
		b.WriteString(` AND created_at >= ?`)
		args = append(args, fromBound)
	}
	if toExclusive != "" {
		b.WriteString(` AND created_at < ?`)
		args = append(args, toExclusive)
	}
	var n int64
	err := s.db.QueryRowContext(ctx, b.String(), args...).Scan(&n)
	return n, err
}

// LedgerStatsBySource sums success amounts grouped by source for a date range (UTC date prefix of created_at or local dates passed as from/to on created_at text).
func (s *Store) LedgerStatsBySource(ctx context.Context, from, to string) ([]LedgerDayStat, error) {
	q := `
SELECT substr(created_at,1,10) AS d, source, COUNT(1), COALESCE(SUM(CASE WHEN status='success' THEN amount ELSE 0 END),0)
FROM credit_ledger
WHERE 1=1`
	args := []any{}
	fromBound, toExclusive := ledgerCreatedBounds(from, to)
	if fromBound != "" {
		q += ` AND created_at >= ?`
		args = append(args, fromBound)
	}
	if toExclusive != "" {
		q += ` AND created_at < ?`
		args = append(args, toExclusive)
	}
	q += ` GROUP BY d, source ORDER BY d DESC, source`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LedgerDayStat
	for rows.Next() {
		var st LedgerDayStat
		if err := rows.Scan(&st.Date, &st.Source, &st.Count, &st.Amount); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// SumLedgerSuccessByDate totals successful grants for one calendar day prefix.
func (s *Store) SumLedgerSuccessByDate(ctx context.Context, date string) (float64, int64, error) {
	var amount sql.NullFloat64
	var n int64
	fromBound, toExclusive := ledgerCreatedBounds(date, date)
	err := s.db.QueryRowContext(ctx, `
SELECT COALESCE(SUM(amount),0), COUNT(1)
FROM credit_ledger
WHERE status='success' AND created_at >= ? AND created_at < ?
`, fromBound, toExclusive).Scan(&amount, &n)
	return amount.Float64, n, err
}

// SummarizeLedgerByStatus totals rows matching the filter (ignores Limit/Offset).
// When Status is set, only that status contributes; success_* may be zero if Status!=success.
func (s *Store) SummarizeLedgerByStatus(ctx context.Context, f LedgerFilter) (LedgerStatusSummary, error) {
	var out LedgerStatusSummary
	q := `
SELECT
  COALESCE(SUM(CASE WHEN status='success' THEN 1 ELSE 0 END),0),
  COALESCE(SUM(CASE WHEN status='success' THEN amount ELSE 0 END),0),
  COALESCE(SUM(CASE WHEN status='failed' THEN 1 ELSE 0 END),0),
  COALESCE(SUM(CASE WHEN status='skipped' THEN 1 ELSE 0 END),0)
FROM credit_ledger WHERE 1=1`
	args := []any{}
	if f.Source != "" {
		q += ` AND source = ?`
		args = append(args, f.Source)
	}
	if f.Status != "" {
		q += ` AND status = ?`
		args = append(args, f.Status)
	}
	if f.UserID > 0 {
		q += ` AND user_id = ?`
		args = append(args, f.UserID)
	}
	fromBound, toExclusive := ledgerCreatedBounds(f.From, f.To)
	if fromBound != "" {
		q += ` AND created_at >= ?`
		args = append(args, fromBound)
	}
	if toExclusive != "" {
		q += ` AND created_at < ?`
		args = append(args, toExclusive)
	}
	err := s.db.QueryRowContext(ctx, q, args...).Scan(
		&out.SuccessCount, &out.SuccessAmount, &out.FailedCount, &out.SkippedCount,
	)
	return out, err
}

// BackfillLedgerFromLegacy copies historical checkin/lottery success rows.
// Safe to run multiple times (idempotency keys).
func (s *Store) BackfillLedgerFromLegacy(ctx context.Context) (inserted int, err error) {
	// MaxOpenConns(1): fully materialize source rows before nested ledger queries.
	type legacyRow struct {
		id, uid              int64
		date, label, created string
		amount               float64
		source               string
	}
	var legacy []legacyRow

	rows, err := s.db.QueryContext(ctx, `
SELECT id, user_id, checkin_date, amount, created_at FROM checkin_records ORDER BY id`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var r legacyRow
		r.source = LedgerSourceCheckin
		if err := rows.Scan(&r.id, &r.uid, &r.date, &r.amount, &r.created); err != nil {
			rows.Close()
			return inserted, err
		}
		legacy = append(legacy, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return inserted, err
	}

	lrows, err := s.db.QueryContext(ctx, `
SELECT id, user_id, draw_date, amount, prize_label, created_at FROM lottery_draws WHERE amount > 0 ORDER BY id`)
	if err != nil {
		return inserted, err
	}
	for lrows.Next() {
		var r legacyRow
		r.source = LedgerSourceLottery
		if err := lrows.Scan(&r.id, &r.uid, &r.date, &r.amount, &r.label, &r.created); err != nil {
			lrows.Close()
			return inserted, err
		}
		legacy = append(legacy, r)
	}
	err = lrows.Err()
	lrows.Close()
	if err != nil {
		return inserted, err
	}

	for _, r := range legacy {
		var key, ref, notes string
		if r.source == LedgerSourceCheckin {
			key = fmt.Sprintf("checkin-%d-%s", r.uid, r.date)
			ref = fmt.Sprintf("checkin:%d", r.id)
			notes = "backfill checkin"
		} else {
			key = fmt.Sprintf("lottery-%d-%s", r.uid, r.date)
			ref = fmt.Sprintf("lottery:%d", r.id)
			notes = "backfill lottery:" + r.label
		}
		ok, err := s.HasLedgerIdem(ctx, key)
		if err != nil {
			return inserted, err
		}
		if ok {
			continue
		}
		ts := time.Now().UTC()
		if parsed, e := time.Parse(time.RFC3339Nano, r.created); e == nil {
			ts = parsed
		}
		if _, err := s.InsertLedger(ctx, LedgerEntry{
			UserID: r.uid, Source: r.source, SourceRef: ref,
			Amount: r.amount, IdempotencyKey: key, Status: LedgerStatusSuccess,
			Notes: notes, CreatedAt: ts,
		}); err != nil {
			if strings.Contains(err.Error(), "duplicate") {
				continue
			}
			return inserted, err
		}
		inserted++
	}
	return inserted, nil
}

func scanLedger(row *sql.Row) (*LedgerEntry, error) {
	var e LedgerEntry
	var created string
	err := row.Scan(&e.ID, &e.UserID, &e.Source, &e.SourceRef, &e.Amount, &e.IdempotencyKey, &e.Status, &e.Notes, &e.Error, &created)
	if err != nil {
		if errorsIsNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	if t, err := time.Parse(time.RFC3339Nano, created); err == nil {
		e.CreatedAt = t
	}
	return &e, nil
}

func scanLedgerRows(rows *sql.Rows) (*LedgerEntry, error) {
	var e LedgerEntry
	var created string
	if err := rows.Scan(&e.ID, &e.UserID, &e.Source, &e.SourceRef, &e.Amount, &e.IdempotencyKey, &e.Status, &e.Notes, &e.Error, &created); err != nil {
		return nil, err
	}
	if t, err := time.Parse(time.RFC3339Nano, created); err == nil {
		e.CreatedAt = t
	}
	return &e, nil
}

func errorsIsNoRows(err error) bool {
	return err == sql.ErrNoRows
}
