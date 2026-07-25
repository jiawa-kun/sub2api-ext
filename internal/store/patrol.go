package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// PatrolRun is one account-patrol execution summary.
type PatrolRun struct {
	ID          int64
	TriggerType string
	Status      string
	StartedAt   time.Time
	FinishedAt  time.Time
	StatsJSON   string
	Error       string
	LogJSON     string
}

func (s *Store) InsertPatrolRun(ctx context.Context, run PatrolRun) (int64, error) {
	started := run.StartedAt
	if started.IsZero() {
		started = time.Now().UTC()
	}
	stats := run.StatsJSON
	if stats == "" {
		stats = "{}"
	}
	logs := run.LogJSON
	if logs == "" {
		logs = "[]"
	}
	res, err := s.db.ExecContext(ctx, `
INSERT INTO patrol_runs (trigger_type, status, started_at, finished_at, stats_json, error, log_json)
VALUES (?, ?, ?, ?, ?, ?, ?)
`, run.TriggerType, run.Status, started.UTC().Format(time.RFC3339Nano), "", stats, run.Error, logs)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdatePatrolRun(ctx context.Context, id int64, status, statsJSON, logJSON, errText string, finishedAt time.Time) error {
	if statsJSON == "" {
		statsJSON = "{}"
	}
	if logJSON == "" {
		logJSON = "[]"
	}
	finished := ""
	if !finishedAt.IsZero() {
		finished = finishedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE patrol_runs
SET status = ?, finished_at = ?, stats_json = ?, error = ?, log_json = ?
WHERE id = ?
`, status, finished, statsJSON, errText, logJSON, id)
	return err
}

func (s *Store) GetPatrolRun(ctx context.Context, id int64) (*PatrolRun, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, trigger_type, status, started_at, finished_at, stats_json, error, log_json
FROM patrol_runs WHERE id = ?
`, id)
	return scanPatrolRun(row)
}

func (s *Store) LatestPatrolRun(ctx context.Context) (*PatrolRun, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, trigger_type, status, started_at, finished_at, stats_json, error, log_json
FROM patrol_runs
ORDER BY id DESC
LIMIT 1
`)
	run, err := scanPatrolRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return run, err
}

func (s *Store) ListPatrolRuns(ctx context.Context, limit int) ([]PatrolRun, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, trigger_type, status, started_at, finished_at, stats_json, error, log_json
FROM patrol_runs
ORDER BY id DESC
LIMIT ?
`, limit)
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

func (s *Store) TrimPatrolRuns(ctx context.Context, keep int) error {
	if keep <= 0 {
		keep = 50
	}
	_, err := s.db.ExecContext(ctx, `
DELETE FROM patrol_runs
WHERE id NOT IN (
  SELECT id FROM patrol_runs ORDER BY id DESC LIMIT ?
)
`, keep)
	return err
}

func scanPatrolRun(row *sql.Row) (*PatrolRun, error) {
	var (
		id                    int64
		trigger, status       string
		startedRaw, finishRaw string
		statsJSON, errText    string
		logJSON               string
	)
	if err := row.Scan(&id, &trigger, &status, &startedRaw, &finishRaw, &statsJSON, &errText, &logJSON); err != nil {
		return nil, err
	}
	return &PatrolRun{
		ID:          id,
		TriggerType: trigger,
		Status:      status,
		StartedAt:   parseTime(startedRaw),
		FinishedAt:  parseTime(finishRaw),
		StatsJSON:   statsJSON,
		Error:       errText,
		LogJSON:     logJSON,
	}, nil
}

func parseTime(raw string) time.Time {
	raw = string([]byte(raw))
	if raw == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t
	}
	return time.Time{}
}
