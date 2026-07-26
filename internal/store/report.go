package store

import (
	"context"
	"database/sql"
)

// LotteryStatsByDate returns draws / winners / credited amount for one day.
// It exists so the daily report can describe a single day without pulling
// every draw row into memory.
func (s *Store) LotteryStatsByDate(ctx context.Context, drawDate string) (LotteryTotals, error) {
	var out LotteryTotals
	var total sql.NullFloat64
	err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*),
       COALESCE(SUM(CASE WHEN amount > 0 THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(amount), 0)
FROM lottery_draws
WHERE draw_date = ?
`, drawDate).Scan(&out.Draws, &out.Winners, &total)
	if err != nil {
		return out, err
	}
	out.TotalAmount = total.Float64
	return out, nil
}

// CountPatrolAccountStates returns how many accounts currently carry at least
// one consecutive failure. The daily report only needs the count, not rows.
func (s *Store) CountPatrolAccountStates(ctx context.Context, onlyProblem bool) (int64, error) {
	query := `SELECT COUNT(*) FROM patrol_account_state`
	if onlyProblem {
		query += ` WHERE consecutive_fail > 0`
	}
	var n int64
	err := s.db.QueryRowContext(ctx, query).Scan(&n)
	return n, err
}

// PatrolRunsBetween returns runs whose started_at falls inside [fromUTC, toUTC).
// Both bounds are RFC3339 strings in UTC; started_at is stored the same way so
// lexical comparison is correct.
func (s *Store) PatrolRunsBetween(ctx context.Context, fromUTC, toUTC string) ([]PatrolRun, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, trigger_type, status, started_at, finished_at, stats_json, error, log_json
FROM patrol_runs
WHERE started_at >= ? AND started_at < ?
ORDER BY id ASC
`, fromUTC, toUTC)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PatrolRun
	for rows.Next() {
		var (
			id                    int64
			trigger, status       string
			startedRaw, finishRaw string
			statsJSON, errText    string
			logJSON               string
		)
		if err := rows.Scan(&id, &trigger, &status, &startedRaw, &finishRaw, &statsJSON, &errText, &logJSON); err != nil {
			return nil, err
		}
		out = append(out, PatrolRun{
			ID:          id,
			TriggerType: trigger,
			Status:      status,
			StartedAt:   parseTime(startedRaw),
			FinishedAt:  parseTime(finishRaw),
			StatsJSON:   statsJSON,
			Error:       errText,
			LogJSON:     logJSON,
		})
	}
	return out, rows.Err()
}
