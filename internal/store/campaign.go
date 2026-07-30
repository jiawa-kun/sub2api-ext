package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

const (
	CampaignStatusDraft      = "draft"
	CampaignStatusActive     = "active"
	CampaignStatusSettled    = "settled"
	CampaignStatusCancelled  = "cancelled"
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
	ID          int64
	Name        string
	Board       string
	StartDate   string
	EndDate     string
	TopN        int
	RewardsJSON string
	BudgetCap   float64
	Status      string
	SettledAt   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// RankCampaignAward is one settled grant row.
type RankCampaignAward struct {
	ID         int64
	CampaignID int64
	UserID     int64
	Rank       int
	Amount     float64
	LedgerID   int64
	Status     string
	CreatedAt  time.Time
}

func (s *Store) ensureCampaignSchema() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS rank_campaigns (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  board TEXT NOT NULL DEFAULT 'rewards',
  start_date TEXT NOT NULL,
  end_date TEXT NOT NULL,
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
  user_id INTEGER NOT NULL,
  rank INTEGER NOT NULL,
  amount REAL NOT NULL DEFAULT 0,
  ledger_id INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'success',
  created_at TEXT NOT NULL,
  UNIQUE(campaign_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_rank_awards_campaign ON rank_campaign_awards(campaign_id);
`)
	return err
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
	if c.TopN <= 0 {
		c.TopN = 10
	}
	if c.RewardsJSON == "" {
		c.RewardsJSON = "[]"
	}
	res, err := s.db.ExecContext(ctx, `
INSERT INTO rank_campaigns(name, board, start_date, end_date, top_n, rewards_json, budget_cap, status, settled_at, created_at, updated_at)
VALUES(?,?,?,?,?,?,?,?, '', ?, ?)
`, c.Name, c.Board, c.StartDate, c.EndDate, c.TopN, c.RewardsJSON, c.BudgetCap, c.Status, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateRankCampaign(ctx context.Context, c RankCampaign) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `
UPDATE rank_campaigns SET name=?, board=?, start_date=?, end_date=?, top_n=?, rewards_json=?, budget_cap=?, status=?, updated_at=?
WHERE id=? AND status IN ('draft','active')
`, c.Name, c.Board, c.StartDate, c.EndDate, c.TopN, c.RewardsJSON, c.BudgetCap, c.Status, now, c.ID)
	return err
}

func (s *Store) GetRankCampaign(ctx context.Context, id int64) (*RankCampaign, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, name, board, start_date, end_date, top_n, rewards_json, budget_cap, status, settled_at, created_at, updated_at
FROM rank_campaigns WHERE id=?
`, id)
	return scanCampaign(row)
}

func (s *Store) ListRankCampaigns(ctx context.Context, limit int) ([]RankCampaign, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, name, board, start_date, end_date, top_n, rewards_json, budget_cap, status, settled_at, created_at, updated_at
FROM rank_campaigns ORDER BY id DESC LIMIT ?
`, limit)
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

func (s *Store) ListActiveRankCampaigns(ctx context.Context) ([]RankCampaign, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, name, board, start_date, end_date, top_n, rewards_json, budget_cap, status, settled_at, created_at, updated_at
FROM rank_campaigns WHERE status='active' ORDER BY id DESC
`)
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
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `
UPDATE rank_campaigns SET status=?, settled_at=?, updated_at=? WHERE id=?
`, CampaignStatusSettled, now, now, id)
	return err
}

func (s *Store) InsertCampaignAward(ctx context.Context, a RankCampaignAward) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx, `
INSERT INTO rank_campaign_awards(campaign_id, user_id, rank, amount, ledger_id, status, created_at)
VALUES(?,?,?,?,?,?,?)
`, a.CampaignID, a.UserID, a.Rank, a.Amount, a.LedgerID, a.Status, now)
	if err != nil {
		if isUniqueViolation(err) {
			return 0, fmt.Errorf("award already exists")
		}
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) ListCampaignAwards(ctx context.Context, campaignID int64) ([]RankCampaignAward, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, campaign_id, user_id, rank, amount, ledger_id, status, created_at
FROM rank_campaign_awards WHERE campaign_id=? ORDER BY rank ASC
`, campaignID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RankCampaignAward
	for rows.Next() {
		var a RankCampaignAward
		var created string
		if err := rows.Scan(&a.ID, &a.CampaignID, &a.UserID, &a.Rank, &a.Amount, &a.LedgerID, &a.Status, &created); err != nil {
			return nil, err
		}
		if t, err := time.Parse(time.RFC3339Nano, created); err == nil {
			a.CreatedAt = t
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func scanCampaign(row *sql.Row) (*RankCampaign, error) {
	var c RankCampaign
	var created, updated string
	err := row.Scan(&c.ID, &c.Name, &c.Board, &c.StartDate, &c.EndDate, &c.TopN, &c.RewardsJSON, &c.BudgetCap, &c.Status, &c.SettledAt, &created, &updated)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if t, e := time.Parse(time.RFC3339Nano, created); e == nil {
		c.CreatedAt = t
	}
	if t, e := time.Parse(time.RFC3339Nano, updated); e == nil {
		c.UpdatedAt = t
	}
	return &c, nil
}

func scanCampaignRows(rows *sql.Rows) (*RankCampaign, error) {
	var c RankCampaign
	var created, updated string
	if err := rows.Scan(&c.ID, &c.Name, &c.Board, &c.StartDate, &c.EndDate, &c.TopN, &c.RewardsJSON, &c.BudgetCap, &c.Status, &c.SettledAt, &created, &updated); err != nil {
		return nil, err
	}
	if t, e := time.Parse(time.RFC3339Nano, created); e == nil {
		c.CreatedAt = t
	}
	if t, e := time.Parse(time.RFC3339Nano, updated); e == nil {
		c.UpdatedAt = t
	}
	return &c, nil
}
