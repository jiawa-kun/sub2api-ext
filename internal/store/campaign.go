package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	CampaignStatusDraft      = "draft"
	CampaignStatusActive     = "active"
	CampaignStatusSettled    = "settled"
	CampaignStatusCancelled  = "cancelled"
	CampaignStatusPartial    = "partial"
	CampaignPeriodPending    = "pending"
	CampaignPeriodRunning    = "running"
	CampaignPeriodSettled    = "settled"
	CampaignPeriodPartial    = "partial"
	CampaignPeriodEmpty      = "empty"
	CampaignPeriodFailed     = "failed"
	CampaignBoardRewards     = "rewards"
	CampaignBoardConsumption = "consumption"
)

// RankRewardRule maps ranks to amounts.
type RankRewardRule struct {
	Rank     int     `json:"rank,omitempty"`
	RankFrom int     `json:"rank_from,omitempty"`
	RankTo   int     `json:"rank_to,omitempty"`
	Amount   float64 `json:"amount"`
}

// RankCampaign is a settlement activity.
type RankCampaign struct {
	ID             int64
	Name           string
	Board          string
	StartDate      string
	EndDate        string
	Frequency      string
	SettlementTime string
	TopN           int
	RewardsJSON    string
	BudgetCap      float64
	Status         string
	SettledAt      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	AwardCount     int
}

// RankCampaignAward is one settled grant row.
type RankCampaignAward struct {
	ID         int64
	CampaignID int64
	PeriodKey  string
	UserID     int64
	Rank       int
	Amount     float64
	LedgerID   int64
	Status     string
	Error      string
	CreatedAt  time.Time
}

// RankCampaignFilter controls admin campaign listing.
type RankCampaignFilter struct {
	Keyword   string
	Board     string
	Frequency string
	Status    string
	Limit     int
	Offset    int
}

// RankCampaignAwardFilter controls award detail listing.
type RankCampaignAwardFilter struct {
	PeriodKey string
	Status    string
	UserID    int64
	Limit     int
	Offset    int
}

type RankCampaignAwardSummary struct {
	TotalAmount  float64
	SuccessCount int
	FailedCount  int
	SkippedCount int
}

// RankCampaignPeriod records one independent settlement window.
type RankCampaignPeriod struct {
	ID         int64
	CampaignID int64
	PeriodKey  string
	StartDate  string
	EndDate    string
	Status     string
	Error      string
	SettledAt  string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (s *Store) ensureCampaignSchema() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS rank_campaigns (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  board TEXT NOT NULL DEFAULT 'rewards',
  start_date TEXT NOT NULL,
  end_date TEXT NOT NULL,
  frequency TEXT NOT NULL DEFAULT 'once',
  settlement_time TEXT NOT NULL DEFAULT '03:00',
  top_n INTEGER NOT NULL DEFAULT 10,
  rewards_json TEXT NOT NULL DEFAULT '[]',
  budget_cap REAL NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'draft',
  settled_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_rank_campaigns_status ON rank_campaigns(status);
CREATE TABLE IF NOT EXISTS rank_campaign_awards (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  campaign_id INTEGER NOT NULL,
  period_key TEXT NOT NULL DEFAULT 'once',
  user_id INTEGER NOT NULL,
  rank INTEGER NOT NULL,
  amount REAL NOT NULL DEFAULT 0,
  ledger_id INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'success',
  error_text TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  UNIQUE(campaign_id, period_key, user_id)
);
CREATE INDEX IF NOT EXISTS idx_rank_awards_campaign ON rank_campaign_awards(campaign_id);
CREATE TABLE IF NOT EXISTS rank_campaign_periods (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  campaign_id INTEGER NOT NULL,
  period_key TEXT NOT NULL,
  start_date TEXT NOT NULL,
  end_date TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending',
  error TEXT NOT NULL DEFAULT '',
  settled_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(campaign_id, period_key)
);
CREATE INDEX IF NOT EXISTS idx_rank_campaign_periods_campaign ON rank_campaign_periods(campaign_id, period_key);
`)
	if err != nil {
		return err
	}
	if err := s.ensureCampaignColumn("rank_campaigns", "frequency", "TEXT NOT NULL DEFAULT 'once'"); err != nil {
		return err
	}
	if err := s.ensureCampaignColumn("rank_campaigns", "settlement_time", "TEXT NOT NULL DEFAULT '03:00'"); err != nil {
		return err
	}
	hasPeriod, err := s.campaignColumnExists("rank_campaign_awards", "period_key")
	if err != nil {
		return err
	}
	if !hasPeriod {
		if err := s.migrateCampaignAwardsPeriod(); err != nil {
			return err
		}
	}
	if err := s.ensureCampaignColumn("rank_campaign_awards", "error_text", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	_, err = s.db.Exec(`
CREATE INDEX IF NOT EXISTS idx_rank_awards_campaign ON rank_campaign_awards(campaign_id);
CREATE INDEX IF NOT EXISTS idx_rank_awards_campaign_period ON rank_campaign_awards(campaign_id, period_key, rank);
`)
	return err
}

func (s *Store) campaignColumnExists(table, column string) (bool, error) {
	rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *Store) ensureCampaignColumn(table, column, definition string) error {
	has, err := s.campaignColumnExists(table, column)
	if err != nil || has {
		return err
	}
	_, err = s.db.Exec("ALTER TABLE " + table + " ADD COLUMN " + column + " " + definition)
	return err
}

func (s *Store) migrateCampaignAwardsPeriod() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DROP TABLE IF EXISTS rank_campaign_awards_new`); err != nil {
		return err
	}
	if _, err := tx.Exec(`
CREATE TABLE rank_campaign_awards_new (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  campaign_id INTEGER NOT NULL,
  period_key TEXT NOT NULL DEFAULT 'once',
  user_id INTEGER NOT NULL,
  rank INTEGER NOT NULL,
  amount REAL NOT NULL DEFAULT 0,
  ledger_id INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'success',
  error_text TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  UNIQUE(campaign_id, period_key, user_id)
)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`
INSERT INTO rank_campaign_awards_new(id, campaign_id, period_key, user_id, rank, amount, ledger_id, status, created_at)
SELECT id, campaign_id, 'once', user_id, rank, amount, ledger_id, status, created_at
FROM rank_campaign_awards`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE rank_campaign_awards`); err != nil {
		return err
	}
	if _, err := tx.Exec(`ALTER TABLE rank_campaign_awards_new RENAME TO rank_campaign_awards`); err != nil {
		return err
	}
	return tx.Commit()
}

func (c *RankCampaign) ParseRewards() ([]RankRewardRule, error) {
	var rules []RankRewardRule
	if c.RewardsJSON == "" {
		return rules, nil
	}
	if err := json.Unmarshal([]byte(c.RewardsJSON), &rules); err != nil {
		return nil, err
	}
	return rules, nil
}

// AmountForRank resolves reward for a 1-based rank.
func AmountForRank(rules []RankRewardRule, rank int) float64 {
	exact := -1.0
	hasExact := false
	rangeAmt := 0.0
	hasRange := false
	for _, r := range rules {
		if r.Rank > 0 && r.Rank == rank {
			exact = r.Amount
			hasExact = true
		}
		if r.RankFrom > 0 && r.RankTo >= r.RankFrom && rank >= r.RankFrom && rank <= r.RankTo {
			rangeAmt = r.Amount
			hasRange = true
		}
	}
	if hasExact {
		return exact
	}
	if hasRange {
		return rangeAmt
	}
	return 0
}

func (s *Store) CreateRankCampaign(ctx context.Context, c RankCampaign) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if c.Status == "" {
		c.Status = CampaignStatusDraft
	}
	if c.Board == "" {
		c.Board = CampaignBoardRewards
	}
	c.Frequency = NormalizeCampaignFrequency(c.Frequency)
	c.SettlementTime = NormalizeCampaignSettlementTime(c.SettlementTime)
	if c.TopN <= 0 {
		c.TopN = 10
	}
	if c.RewardsJSON == "" {
		c.RewardsJSON = "[]"
	}
	res, err := s.db.ExecContext(ctx, `
	INSERT INTO rank_campaigns(name, board, start_date, end_date, frequency, settlement_time, top_n, rewards_json, budget_cap, status, settled_at, created_at, updated_at)
	VALUES(?,?,?,?,?,?,?,?,?,?,'',?,?)
	`, c.Name, c.Board, c.StartDate, c.EndDate, c.Frequency, c.SettlementTime, c.TopN, c.RewardsJSON, c.BudgetCap, c.Status, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateRankCampaign(ctx context.Context, c RankCampaign) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	c.Frequency = NormalizeCampaignFrequency(c.Frequency)
	c.SettlementTime = NormalizeCampaignSettlementTime(c.SettlementTime)
	_, err := s.db.ExecContext(ctx, `
	UPDATE rank_campaigns SET name=?, board=?, start_date=?, end_date=?, frequency=?, settlement_time=?, top_n=?, rewards_json=?, budget_cap=?, status=?, updated_at=?
	WHERE id=? AND status IN ('draft','active')
	`, c.Name, c.Board, c.StartDate, c.EndDate, c.Frequency, c.SettlementTime, c.TopN, c.RewardsJSON, c.BudgetCap, c.Status, now, c.ID)
	return err
}

func (s *Store) GetRankCampaign(ctx context.Context, id int64) (*RankCampaign, error) {
	row := s.db.QueryRowContext(ctx, `
	SELECT id, name, board, start_date, end_date, frequency, settlement_time, top_n, rewards_json, budget_cap, status, settled_at, created_at, updated_at
FROM rank_campaigns WHERE id=?
`, id)
	return scanCampaign(row)
}

func (s *Store) ListRankCampaigns(ctx context.Context, limit int) ([]RankCampaign, error) {
	items, _, err := s.ListRankCampaignsPage(ctx, RankCampaignFilter{Limit: limit})
	return items, err
}

func (s *Store) ListRankCampaignsPage(ctx context.Context, filter RankCampaignFilter) ([]RankCampaign, int, error) {
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	if filter.Limit > 1000 {
		filter.Limit = 1000
	}
	where := []string{"1=1"}
	args := make([]any, 0, 8)
	if keyword := strings.TrimSpace(filter.Keyword); keyword != "" {
		where = append(where, "c.name LIKE ?")
		args = append(args, "%"+keyword+"%")
	}
	if board := strings.TrimSpace(filter.Board); board != "" {
		where = append(where, "c.board=?")
		args = append(args, board)
	}
	if frequency := strings.TrimSpace(filter.Frequency); frequency != "" {
		where = append(where, "c.frequency=?")
		args = append(args, frequency)
	}
	if status := strings.TrimSpace(filter.Status); status != "" {
		where = append(where, "c.status=?")
		args = append(args, status)
	}
	whereSQL := strings.Join(where, " AND ")
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM rank_campaigns c WHERE "+whereSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	queryArgs := append(append([]any{}, args...), filter.Limit, filter.Offset)
	rows, err := s.db.QueryContext(ctx, `
SELECT c.id, c.name, c.board, c.start_date, c.end_date, c.frequency, c.settlement_time, c.top_n, c.rewards_json, c.budget_cap, c.status, c.settled_at, c.created_at, c.updated_at, COUNT(a.id)
FROM rank_campaigns c
LEFT JOIN rank_campaign_awards a ON a.campaign_id=c.id
WHERE `+whereSQL+`
GROUP BY c.id
ORDER BY c.id DESC
LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []RankCampaign
	for rows.Next() {
		c, err := scanCampaignRowsWithAwardCount(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *c)
	}
	return out, total, rows.Err()
}

// ListActiveRankCampaigns returns active campaigns whose date window covers today (YYYY-MM-DD).
// If today is empty, only status=active is filtered (no date window).
func (s *Store) ListActiveRankCampaigns(ctx context.Context, today string) ([]RankCampaign, error) {
	q := `
	SELECT id, name, board, start_date, end_date, frequency, settlement_time, top_n, rewards_json, budget_cap, status, settled_at, created_at, updated_at
FROM rank_campaigns WHERE status='active'`
	args := []any{}
	if today != "" {
		q += ` AND start_date <= ? AND end_date >= ?`
		args = append(args, today, today)
	}
	q += ` ORDER BY id DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RankCampaign
	for rows.Next() {
		c, err := scanCampaignRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (s *Store) MarkCampaignSettled(ctx context.Context, id int64) error {
	return s.MarkCampaignStatus(ctx, id, CampaignStatusSettled, true)
}

// MarkCampaignStatus updates campaign status. setSettledAt stamps settled_at when true.
func (s *Store) MarkCampaignStatus(ctx context.Context, id int64, status string, setSettledAt bool) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if setSettledAt {
		_, err := s.db.ExecContext(ctx, `
UPDATE rank_campaigns SET status=?, settled_at=?, updated_at=? WHERE id=?
`, status, now, now, id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE rank_campaigns SET status=?, updated_at=? WHERE id=?
`, status, now, id)
	return err
}

// CampaignAwardMap returns awards keyed by user_id.
func (s *Store) CampaignAwardMap(ctx context.Context, campaignID int64) (map[int64]RankCampaignAward, error) {
	list, err := s.ListCampaignAwards(ctx, campaignID)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]RankCampaignAward, len(list))
	for _, a := range list {
		out[a.UserID] = a
	}
	return out, nil
}

// CampaignAwardMapForPeriod returns awards keyed by user for one period.
func (s *Store) CampaignAwardMapForPeriod(ctx context.Context, campaignID int64, periodKey string) (map[int64]RankCampaignAward, error) {
	list, err := s.ListCampaignAwardsForPeriod(ctx, campaignID, periodKey)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]RankCampaignAward, len(list))
	for _, a := range list {
		out[a.UserID] = a
	}
	return out, nil
}

// UpdateCampaignAward rewrites one award row (for retry after failure).
func (s *Store) UpdateCampaignAward(ctx context.Context, id int64, amount float64, ledgerID int64, status string) error {
	return s.UpdateCampaignAwardResult(ctx, id, amount, ledgerID, status, "")
}

func (s *Store) UpdateCampaignAwardResult(ctx context.Context, id int64, amount float64, ledgerID int64, status, errorText string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE rank_campaign_awards SET amount=?, ledger_id=?, status=?, error_text=? WHERE id=?
`, amount, ledgerID, status, errorText, id)
	return err
}

func (s *Store) InsertCampaignAward(ctx context.Context, a RankCampaignAward) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if strings.TrimSpace(a.PeriodKey) == "" {
		a.PeriodKey = CampaignFrequencyOnce
	}
	res, err := s.db.ExecContext(ctx, `
	INSERT INTO rank_campaign_awards(campaign_id, period_key, user_id, rank, amount, ledger_id, status, error_text, created_at)
	VALUES(?,?,?,?,?,?,?,?,?)
	`, a.CampaignID, a.PeriodKey, a.UserID, a.Rank, a.Amount, a.LedgerID, a.Status, a.Error, now)
	if err != nil {
		if isUniqueViolation(err) {
			return 0, fmt.Errorf("award already exists")
		}
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) ListCampaignAwards(ctx context.Context, campaignID int64) ([]RankCampaignAward, error) {
	items, _, _, err := s.ListCampaignAwardsPage(ctx, campaignID, RankCampaignAwardFilter{Limit: 100000})
	return items, err
}

func (s *Store) ListCampaignAwardsForPeriod(ctx context.Context, campaignID int64, periodKey string) ([]RankCampaignAward, error) {
	items, _, _, err := s.ListCampaignAwardsPage(ctx, campaignID, RankCampaignAwardFilter{PeriodKey: periodKey, Limit: 100000})
	return items, err
}

func (s *Store) ListCampaignAwardsPage(ctx context.Context, campaignID int64, filter RankCampaignAwardFilter) ([]RankCampaignAward, int, RankCampaignAwardSummary, error) {
	if filter.Limit <= 0 {
		filter.Limit = 10
	}
	if filter.Limit > 100000 {
		filter.Limit = 100000
	}
	where := []string{"campaign_id=?"}
	args := []any{campaignID}
	if periodKey := strings.TrimSpace(filter.PeriodKey); periodKey != "" {
		where = append(where, "period_key=?")
		args = append(args, periodKey)
	}
	if status := strings.TrimSpace(filter.Status); status != "" {
		where = append(where, "status=?")
		args = append(args, status)
	}
	if filter.UserID > 0 {
		where = append(where, "user_id=?")
		args = append(args, filter.UserID)
	}
	whereSQL := strings.Join(where, " AND ")
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM rank_campaign_awards WHERE "+whereSQL, args...).Scan(&total); err != nil {
		return nil, 0, RankCampaignAwardSummary{}, err
	}
	var summary RankCampaignAwardSummary
	if err := s.db.QueryRowContext(ctx, `
SELECT COALESCE(SUM(CASE WHEN status='success' THEN amount ELSE 0 END),0),
       COALESCE(SUM(CASE WHEN status='success' THEN 1 ELSE 0 END),0),
       COALESCE(SUM(CASE WHEN status='failed' THEN 1 ELSE 0 END),0),
       COALESCE(SUM(CASE WHEN status='skipped' THEN 1 ELSE 0 END),0)
FROM rank_campaign_awards WHERE `+whereSQL, args...).Scan(&summary.TotalAmount, &summary.SuccessCount, &summary.FailedCount, &summary.SkippedCount); err != nil {
		return nil, 0, RankCampaignAwardSummary{}, err
	}
	queryArgs := append(append([]any{}, args...), filter.Limit, filter.Offset)
	rows, err := s.db.QueryContext(ctx, `
SELECT id, campaign_id, period_key, user_id, rank, amount, ledger_id, status, error_text, created_at
FROM rank_campaign_awards WHERE `+whereSQL+` ORDER BY created_at DESC, rank ASC LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return nil, 0, RankCampaignAwardSummary{}, err
	}
	defer rows.Close()
	var out []RankCampaignAward
	for rows.Next() {
		var a RankCampaignAward
		var created string
		if err := rows.Scan(&a.ID, &a.CampaignID, &a.PeriodKey, &a.UserID, &a.Rank, &a.Amount, &a.LedgerID, &a.Status, &a.Error, &created); err != nil {
			return nil, 0, RankCampaignAwardSummary{}, err
		}
		if t, err := time.Parse(time.RFC3339Nano, created); err == nil {
			a.CreatedAt = t
		}
		out = append(out, a)
	}
	return out, total, summary, rows.Err()
}

func (s *Store) CampaignAwardCount(ctx context.Context, campaignID int64) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rank_campaign_awards WHERE campaign_id=?`, campaignID).Scan(&count)
	return count, err
}

// DeleteRankCampaignIfNoAwards permanently removes an activity only when it has no award records.
func (s *Store) DeleteRankCampaignIfNoAwards(ctx context.Context, campaignID int64) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM rank_campaigns WHERE id=?`, campaignID).Scan(&exists); err != nil {
		return false, err
	}
	if exists == 0 {
		return false, sql.ErrNoRows
	}
	var awards int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM rank_campaign_awards WHERE campaign_id=?`, campaignID).Scan(&awards); err != nil {
		return false, err
	}
	if awards > 0 {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM rank_campaign_periods WHERE campaign_id=?`, campaignID); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM rank_campaigns WHERE id=?`, campaignID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) GetCampaignPeriod(ctx context.Context, campaignID int64, periodKey string) (*RankCampaignPeriod, error) {
	row := s.db.QueryRowContext(ctx, `
	SELECT id, campaign_id, period_key, start_date, end_date, status, error, settled_at, created_at, updated_at
	FROM rank_campaign_periods WHERE campaign_id=? AND period_key=?
	`, campaignID, periodKey)
	return scanCampaignPeriod(row)
}

func (s *Store) EnsureCampaignPeriod(ctx context.Context, p RankCampaignPeriod) (*RankCampaignPeriod, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if p.Status == "" {
		p.Status = CampaignPeriodPending
	}
	_, err := s.db.ExecContext(ctx, `
	INSERT INTO rank_campaign_periods(campaign_id, period_key, start_date, end_date, status, error, settled_at, created_at, updated_at)
	VALUES(?,?,?,?,?, '', '', ?, ?)
	ON CONFLICT(campaign_id, period_key) DO NOTHING
	`, p.CampaignID, p.PeriodKey, p.StartDate, p.EndDate, p.Status, now, now)
	if err != nil {
		return nil, err
	}
	return s.GetCampaignPeriod(ctx, p.CampaignID, p.PeriodKey)
}

func (s *Store) MarkCampaignPeriodStatus(ctx context.Context, campaignID int64, periodKey, status, errorText string, settled bool) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	settledAt := ""
	if settled {
		settledAt = now
	}
	_, err := s.db.ExecContext(ctx, `
	UPDATE rank_campaign_periods SET status=?, error=?, settled_at=?, updated_at=?
	WHERE campaign_id=? AND period_key=?
	`, status, errorText, settledAt, now, campaignID, periodKey)
	return err
}

func scanCampaign(row *sql.Row) (*RankCampaign, error) {
	var c RankCampaign
	var created, updated string
	err := row.Scan(&c.ID, &c.Name, &c.Board, &c.StartDate, &c.EndDate, &c.Frequency, &c.SettlementTime, &c.TopN, &c.RewardsJSON, &c.BudgetCap, &c.Status, &c.SettledAt, &created, &updated)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if t, e := time.Parse(time.RFC3339Nano, created); e == nil {
		c.CreatedAt = t
	}
	c.Frequency = NormalizeCampaignFrequency(c.Frequency)
	c.SettlementTime = NormalizeCampaignSettlementTime(c.SettlementTime)
	if t, e := time.Parse(time.RFC3339Nano, updated); e == nil {
		c.UpdatedAt = t
	}
	return &c, nil
}

func scanCampaignRows(rows *sql.Rows) (*RankCampaign, error) {
	var c RankCampaign
	var created, updated string
	if err := rows.Scan(&c.ID, &c.Name, &c.Board, &c.StartDate, &c.EndDate, &c.Frequency, &c.SettlementTime, &c.TopN, &c.RewardsJSON, &c.BudgetCap, &c.Status, &c.SettledAt, &created, &updated); err != nil {
		return nil, err
	}
	if t, e := time.Parse(time.RFC3339Nano, created); e == nil {
		c.CreatedAt = t
	}
	c.Frequency = NormalizeCampaignFrequency(c.Frequency)
	c.SettlementTime = NormalizeCampaignSettlementTime(c.SettlementTime)
	if t, e := time.Parse(time.RFC3339Nano, updated); e == nil {
		c.UpdatedAt = t
	}
	return &c, nil
}

func scanCampaignRowsWithAwardCount(rows *sql.Rows) (*RankCampaign, error) {
	var c RankCampaign
	var created, updated string
	if err := rows.Scan(&c.ID, &c.Name, &c.Board, &c.StartDate, &c.EndDate, &c.Frequency, &c.SettlementTime, &c.TopN, &c.RewardsJSON, &c.BudgetCap, &c.Status, &c.SettledAt, &created, &updated, &c.AwardCount); err != nil {
		return nil, err
	}
	if t, e := time.Parse(time.RFC3339Nano, created); e == nil {
		c.CreatedAt = t
	}
	c.Frequency = NormalizeCampaignFrequency(c.Frequency)
	c.SettlementTime = NormalizeCampaignSettlementTime(c.SettlementTime)
	if t, e := time.Parse(time.RFC3339Nano, updated); e == nil {
		c.UpdatedAt = t
	}
	return &c, nil
}

func scanCampaignPeriod(row *sql.Row) (*RankCampaignPeriod, error) {
	var p RankCampaignPeriod
	var created, updated string
	err := row.Scan(&p.ID, &p.CampaignID, &p.PeriodKey, &p.StartDate, &p.EndDate, &p.Status, &p.Error, &p.SettledAt, &created, &updated)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if t, e := time.Parse(time.RFC3339Nano, created); e == nil {
		p.CreatedAt = t
	}
	if t, e := time.Parse(time.RFC3339Nano, updated); e == nil {
		p.UpdatedAt = t
	}
	return &p, nil
}
