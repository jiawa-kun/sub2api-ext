package store

import (
	"context"
	"fmt"
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

// ListRewardRanking aggregates check-in and lottery amounts in [fromDate, toDate].
// Results are ordered by total amount desc, then last activity date desc.
// limit <= 0 returns all rows (used to compute my_rank).
func (s *Store) ListRewardRanking(ctx context.Context, fromDate, toDate string, limit int) ([]RewardRankRow, RewardRankSummary, error) {
	if fromDate == "" || toDate == "" {
		return nil, RewardRankSummary{}, fmt.Errorf("from/to date required")
	}
	q := `
WITH rewards AS (
  SELECT user_id AS user_id, amount AS amount, checkin_date AS d, 'checkin' AS src
  FROM checkin_records
  WHERE checkin_date >= ? AND checkin_date <= ?
  UNION ALL
  SELECT user_id AS user_id, amount AS amount, draw_date AS d, 'lottery' AS src
  FROM lottery_draws
  WHERE draw_date >= ? AND draw_date <= ?
)
SELECT user_id,
       COALESCE(SUM(amount), 0) AS total_amount,
       COALESCE(SUM(CASE WHEN src = 'checkin' THEN amount ELSE 0 END), 0) AS checkin_amount,
       COALESCE(SUM(CASE WHEN src = 'lottery' THEN amount ELSE 0 END), 0) AS lottery_amount,
       COALESCE(SUM(CASE WHEN src = 'checkin' THEN 1 ELSE 0 END), 0) AS checkin_count,
       COALESCE(SUM(CASE WHEN src = 'lottery' THEN 1 ELSE 0 END), 0) AS lottery_count,
       COALESCE(MAX(d), '') AS last_date
FROM rewards
GROUP BY user_id
ORDER BY total_amount DESC, last_date DESC, user_id ASC
`
	args := []any{fromDate, toDate, fromDate, toDate}
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
	var summary RewardRankSummary
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
		summary.TotalAmount += r.TotalAmount
		summary.UserCount++
		if r.TotalAmount > summary.TopAmount {
			summary.TopAmount = r.TotalAmount
		}
	}
	if err := rows.Err(); err != nil {
		return nil, RewardRankSummary{}, err
	}

	// When limit is applied, recompute true totals for the full range so summary
	// cards stay consistent with "total consumption" style boards.
	if limit > 0 {
		full, fullSummary, err := s.ListRewardRanking(ctx, fromDate, toDate, 0)
		if err != nil {
			return out, summary, nil
		}
		_ = full
		return out, fullSummary, nil
	}
	return out, summary, nil
}

// RewardRankOfUser returns 1-based rank for a user in the range, or 0 if absent.
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

