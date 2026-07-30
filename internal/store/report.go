package store

import (
	"context"
	"database/sql"
)

// LotteryStatsByDate returns draws / winners / credited amount for one day.
func (s *Store) LotteryStatsByDate(ctx context.Context, drawDate string) (LotteryTotals, error) {
	m, err := s.LotteryStatsByDates(ctx, []string{drawDate})
	if err != nil {
		return LotteryTotals{}, err
	}
	return m[drawDate], nil
}

// LotteryStatsByDates aggregates lottery totals for multiple draw_date values in one query.
func (s *Store) LotteryStatsByDates(ctx context.Context, dates []string) (map[string]LotteryTotals, error) {
	out := make(map[string]LotteryTotals, len(dates))
	uniq := uniqueNonEmpty(dates)
	for _, d := range uniq {
		out[d] = LotteryTotals{}
	}
	if len(uniq) == 0 {
		return out, nil
	}
	q, args := inQuery(`
SELECT draw_date,
       COUNT(*),
       COALESCE(SUM(CASE WHEN amount > 0 THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(amount), 0)
FROM lottery_draws
WHERE draw_date IN (`, uniq, `)
GROUP BY draw_date`)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var date string
		var totals LotteryTotals
		var amount sql.NullFloat64
		if err := rows.Scan(&date, &totals.Draws, &totals.Winners, &amount); err != nil {
			return nil, err
		}
		totals.TotalAmount = amount.Float64
		out[date] = totals
	}
	return out, rows.Err()
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
// log_json is intentionally omitted: daily report / status summaries only need
// counters and error text, and run logs can be multi-hundred KB.
func (s *Store) PatrolRunsBetween(ctx context.Context, fromUTC, toUTC string) ([]PatrolRun, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, trigger_type, status, started_at, finished_at, stats_json, error
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
		)
		if err := rows.Scan(&id, &trigger, &status, &startedRaw, &finishRaw, &statsJSON, &errText); err != nil {
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
		})
	}
	return out, rows.Err()
}

// StatsByDates aggregates check-in counts/amounts for multiple dates in one query.
func (s *Store) StatsByDates(ctx context.Context, dates []string) (map[string]DayStats, error) {
	out := make(map[string]DayStats, len(dates))
	uniq := uniqueNonEmpty(dates)
	for _, d := range uniq {
		out[d] = DayStats{Date: d}
	}
	if len(uniq) == 0 {
		return out, nil
	}
	q, args := inQuery(`
SELECT checkin_date, COUNT(1), COALESCE(SUM(amount), 0)
FROM checkin_records
WHERE checkin_date IN (`, uniq, `)
GROUP BY checkin_date`)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var st DayStats
		if err := rows.Scan(&st.Date, &st.Count, &st.TotalAmount); err != nil {
			return nil, err
		}
		out[st.Date] = st
	}
	return out, rows.Err()
}

func uniqueNonEmpty(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func inQuery(prefix string, vals []string, suffix string) (string, []any) {
	args := make([]any, 0, len(vals))
	q := prefix
	for i, v := range vals {
		if i > 0 {
			q += ","
		}
		q += "?"
		args = append(args, v)
	}
	q += suffix
	return q, args
}
