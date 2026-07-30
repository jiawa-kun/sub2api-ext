package store

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// RewardRankRow is one user's aggregated check-in + lottery rewards in a range.
type RewardRankRow struct {
	UserID        int64
	TotalAmount   float64
	CheckinAmount float64
	LotteryAmount float64
	CheckinCount  int64
	LotteryCount  int64
	LastDate      string
}

// RewardRankSummary is the range-wide totals for the rewards board.
type RewardRankSummary struct {
	TotalAmount float64
	UserCount   int64
	TopAmount   float64
}

type rankingCacheEntry struct {
	Rows    []RewardRankRow
	Summary RewardRankSummary
	Exp     time.Time
}

const rankingCacheTTL = 30 * time.Second

// rankingCache is process-local and keyed by date range + limit.
// It is safe for concurrent readers; writers replace whole entries.
var (
	rankingCacheMu sync.RWMutex
	rankingCache   = map[string]rankingCacheEntry{}
)

func rankingCacheKey(fromDate, toDate string, limit int) string {
	return fromDate + "|" + toDate + "|" + fmt.Sprintf("%d", limit)
}

func getRankingCache(key string) (rankingCacheEntry, bool) {
	rankingCacheMu.RLock()
	defer rankingCacheMu.RUnlock()
	ent, ok := rankingCache[key]
	if !ok || time.Now().After(ent.Exp) {
		return rankingCacheEntry{}, false
	}
	// Return copies so callers cannot mutate shared slices.
	rows := append([]RewardRankRow(nil), ent.Rows...)
	return rankingCacheEntry{Rows: rows, Summary: ent.Summary, Exp: ent.Exp}, true
}

func putRankingCache(key string, rows []RewardRankRow, summary RewardRankSummary) {
	rankingCacheMu.Lock()
	defer rankingCacheMu.Unlock()
	if len(rankingCache) > 256 {
		now := time.Now()
		for k, v := range rankingCache {
			if now.After(v.Exp) {
				delete(rankingCache, k)
			}
		}
	}
	rankingCache[key] = rankingCacheEntry{
		Rows:    append([]RewardRankRow(nil), rows...),
		Summary: summary,
		Exp:     time.Now().Add(rankingCacheTTL),
	}
}

// InvalidateRankingCache clears ranking cache after reward writes.
func InvalidateRankingCache() {
	rankingCacheMu.Lock()
	rankingCache = map[string]rankingCacheEntry{}
	rankingCacheMu.Unlock()
}

const rewardAggCTE = `
WITH rewards AS (
  SELECT user_id AS user_id, amount AS amount, checkin_date AS d, 'checkin' AS src
  FROM checkin_records
  WHERE checkin_date >= ? AND checkin_date <= ?
  UNION ALL
  SELECT user_id AS user_id, amount AS amount, draw_date AS d, 'lottery' AS src
  FROM lottery_draws
  WHERE draw_date >= ? AND draw_date <= ?
),
agg AS (
  SELECT user_id,
         COALESCE(SUM(amount), 0) AS total_amount,
         COALESCE(SUM(CASE WHEN src = 'checkin' THEN amount ELSE 0 END), 0) AS checkin_amount,
         COALESCE(SUM(CASE WHEN src = 'lottery' THEN amount ELSE 0 END), 0) AS lottery_amount,
         COALESCE(SUM(CASE WHEN src = 'checkin' THEN 1 ELSE 0 END), 0) AS checkin_count,
         COALESCE(SUM(CASE WHEN src = 'lottery' THEN 1 ELSE 0 END), 0) AS lottery_count,
         COALESCE(MAX(d), '') AS last_date
  FROM rewards
  GROUP BY user_id
)
`

// ListRewardRanking aggregates check-in and lottery amounts in [fromDate, toDate].
// Results are ordered by total amount desc, then last activity date desc.
// limit <= 0 returns all rows (used to compute my_rank).
// When limit > 0, rows are limited but summary always covers the full range.
func (s *Store) ListRewardRanking(ctx context.Context, fromDate, toDate string, limit int) ([]RewardRankRow, RewardRankSummary, error) {
	if fromDate == "" || toDate == "" {
		return nil, RewardRankSummary{}, fmt.Errorf("from/to date required")
	}

	cacheKey := rankingCacheKey(fromDate, toDate, limit)
	if ent, ok := getRankingCache(cacheKey); ok {
		return ent.Rows, ent.Summary, nil
	}

	args := []any{fromDate, toDate, fromDate, toDate}
	q := rewardAggCTE + `
SELECT user_id, total_amount, checkin_amount, lottery_amount, checkin_count, lottery_count, last_date
FROM agg
ORDER BY total_amount DESC, last_date DESC, user_id ASC
`
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, RewardRankSummary{}, err
	}
	defer rows.Close()

	var out []RewardRankRow
	for rows.Next() {
		var r RewardRankRow
		if err := rows.Scan(
			&r.UserID,
			&r.TotalAmount,
			&r.CheckinAmount,
			&r.LotteryAmount,
			&r.CheckinCount,
			&r.LotteryCount,
			&r.LastDate,
		); err != nil {
			return nil, RewardRankSummary{}, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, RewardRankSummary{}, err
	}

	var summary RewardRankSummary
	if limit <= 0 {
		for _, r := range out {
			summary.TotalAmount += r.TotalAmount
			summary.UserCount++
			if r.TotalAmount > summary.TopAmount {
				summary.TopAmount = r.TotalAmount
			}
		}
	} else {
		// Cheap full-range summary without shipping every row to Go.
		sumQ := rewardAggCTE + `
SELECT COALESCE(SUM(total_amount), 0),
       COUNT(*),
       COALESCE(MAX(total_amount), 0)
FROM agg
`
		if err := s.db.QueryRowContext(ctx, sumQ, fromDate, toDate, fromDate, toDate).Scan(
			&summary.TotalAmount,
			&summary.UserCount,
			&summary.TopAmount,
		); err != nil {
			return nil, RewardRankSummary{}, err
		}
	}

	putRankingCache(cacheKey, out, summary)
	return out, summary, nil
}

// RewardRankOfUser returns 1-based rank for a user in the range, or 0 if absent.
func (s *Store) RewardRankOfUser(ctx context.Context, fromDate, toDate string, userID int64) (rank int, amount float64, err error) {
	// Prefer rank via SQL so we do not always materialize the full board.
	q := rewardAggCTE + `
, ranked AS (
  SELECT user_id, total_amount,
         ROW_NUMBER() OVER (ORDER BY total_amount DESC, last_date DESC, user_id ASC) AS rn
  FROM agg
)
SELECT rn, total_amount FROM ranked WHERE user_id = ?
`
	var rn int64
	var amt float64
	err = s.db.QueryRowContext(ctx, q, fromDate, toDate, fromDate, toDate, userID).Scan(&rn, &amt)
	if err != nil {
		// Fallback keeps old behavior if window functions are unavailable.
		rows, _, listErr := s.ListRewardRanking(ctx, fromDate, toDate, 0)
		if listErr != nil {
			return 0, 0, listErr
		}
		for i, r := range rows {
			if r.UserID == userID {
				return i + 1, r.TotalAmount, nil
			}
		}
		return 0, 0, nil
	}
	return int(rn), amt, nil
}
