package store

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// RewardRankRow is one user's aggregated check-in + lottery rewards in a range.
type RewardRankRow struct {
	UserID         int64
	TotalAmount    float64
	CheckinAmount  float64
	LotteryAmount  float64
	CheckinCount   int64
	LotteryCount   int64
	LastDate       string
}

// RewardRankSummary is the range-wide totals for the rewards board.
type RewardRankSummary struct {
	TotalAmount  float64
	UserCount    int64
	TopAmount    float64
}

type rankingCacheEntry struct {
	Rows    []RewardRankRow
	Summary RewardRankSummary
	Exp     time.Time
}

var (
	rankingCache     = make(map[string]rankingCacheEntry)
	rankingCacheMu   sync.RWMutex
	rankingCacheTTL  = 30 * time.Second
)

func getCacheKey(fromDate, toDate string, limit int) string {
	return fmt.Sprintf("%s-%s-%d", fromDate, toDate, limit)
}

// ListRewardRanking 优化版：带缓存 + 移除冗余全量查询 + ROW_NUMBER
func (s *Store) ListRewardRanking(ctx context.Context, fromDate, toDate string, limit int) ([]RewardRankRow, RewardRankSummary, error) {
	if fromDate == "" || toDate == "" {
		return nil, RewardRankSummary{}, fmt.Errorf("from/to date required")
	}

	cacheKey := getCacheKey(fromDate, toDate, limit)
	rankingCacheMu.RLock()
	if entry, ok := rankingCache[cacheKey]; ok && time.Now().Before(entry.Exp) {
		rankingCacheMu.RUnlock()
		return entry.Rows, entry.Summary, nil
	}
	rankingCacheMu.RUnlock()

	q := `WITH rewards AS (
  SELECT user_id AS user_id, amount AS amount, checkin_date AS d, 'checkin' AS src
  FROM checkin_records
  WHERE checkin_date >= ? AND checkin_date <= ?
  UNION ALL
  SELECT user_id AS user_id, amount AS amount, draw_date AS d, 'lottery' AS src
  FROM lottery_draws
  WHERE draw_date >= ? AND draw_date <= ?
),
ranked AS (
  SELECT 
    user_id,
    SUM(amount) AS total_amount,
    SUM(CASE WHEN src = 'checkin' THEN amount ELSE 0 END) AS checkin_amount,
    SUM(CASE WHEN src = 'lottery' THEN amount ELSE 0 END) AS lottery_amount,
    SUM(CASE WHEN src = 'checkin' THEN 1 ELSE 0 END) AS checkin_count,
    SUM(CASE WHEN src = 'lottery' THEN 1 ELSE 0 END) AS lottery_count,
    MAX(d) AS last_date,
    ROW_NUMBER() OVER (ORDER BY SUM(amount) DESC, MAX(d) DESC, user_id ASC) AS rn
  FROM rewards
  GROUP BY user_id
)
SELECT 
  user_id,
  total_amount,
  checkin_amount,
  lottery_amount,
  checkin_count,
  lottery_count,
  last_date
FROM ranked
WHERE rn <= ?
ORDER BY rn
`
	args := []any{fromDate, toDate, fromDate, toDate, limit}
	if limit <= 0 {
		q = strings.Replace(q, "WHERE rn <= ?", "", 1)
		args = args[:4]
	}

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, RewardRankSummary{}, err
	}
	defer rows.Close()

	var out []RewardRankRow
	var summary RewardRankSummary
	for rows.Next() {
		var r RewardRankRow
		if err := rows.Scan(&r.UserID, &r.TotalAmount, &r.CheckinAmount, &r.LotteryAmount, &r.CheckinCount, &r.LotteryCount, &r.LastDate); err != nil {
			return nil, RewardRankSummary{}, err
		}
		out = append(out, r)
		summary.TotalAmount += r.TotalAmount
		summary.UserCount++
		if r.TotalAmount > summary.TopAmount {
			summary.TopAmount = r.TotalAmount
		}
	}
	if err := rows.Err(); err != nil {
		return nil, RewardRankSummary{}, err
	}

	entry := rankingCacheEntry{
		Rows:    out,
		Summary: summary,
		Exp:     time.Now().Add(rankingCacheTTL),
	}
	rankingCacheMu.Lock()
	rankingCache[cacheKey] = entry
	rankingCacheMu.Unlock()

	return out, summary, nil
}

// RewardRankOfUser 保持不变
func (s *Store) RewardRankOfUser(ctx context.Context, fromDate, toDate string, userID int64) (rank int, amount float64, err error) {
	rows, _, err := s.ListRewardRanking(ctx, fromDate, toDate, 0)
	if err != nil {
		return 0, 0, err
	}
	for i, r := range rows {
		if r.UserID == userID {
			return i + 1, r.TotalAmount, nil
		}
	}
	return 0, 0, nil
}
