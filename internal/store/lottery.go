package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ErrAlreadyDrawn is returned when a user already used today's draw.
var ErrAlreadyDrawn = errors.New("already drawn today")

// Prize types recorded on a draw.
const (
	PrizeTypeNone    = "none"
	PrizeTypeBalance = "balance"
)

// LotteryDraw is one recorded draw.
type LotteryDraw struct {
	ID         int64     `json:"id"`
	UserID     int64     `json:"user_id"`
	DrawDate   string    `json:"draw_date"`
	PrizeIndex int       `json:"prize_index"`
	PrizeLabel string    `json:"prize_label"`
	PrizeType  string    `json:"prize_type"`
	Amount     float64   `json:"amount"`
	NewBalance float64   `json:"new_balance"`
	CreatedAt  time.Time `json:"created_at"`
}

// GetLotteryDraw returns today's draw for a user, or nil when absent.
func (s *Store) GetLotteryDraw(ctx context.Context, userID int64, drawDate string) (*LotteryDraw, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, user_id, draw_date, prize_index, prize_label, prize_type, amount, new_balance, created_at
FROM lottery_draws
WHERE user_id = ? AND draw_date = ?
`, userID, drawDate)
	d, err := scanLotteryDraw(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return d, nil
}

// ReserveLotteryDraw claims today's slot before any balance is credited.
//
// Insert-first is deliberate: the UNIQUE(user_id, draw_date) index makes
// concurrent draws collide here rather than after crediting, so a double
// request can never produce two grants. The row is finalized by
// FinalizeLotteryDraw and removed by ReleaseLotteryDraw when the grant fails.
func (s *Store) ReserveLotteryDraw(ctx context.Context, userID int64, drawDate string) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx, `
INSERT INTO lottery_draws(user_id, draw_date, prize_label, prize_type, amount, new_balance, created_at)
VALUES(?, ?, '', ?, 0, 0, ?)
`, userID, drawDate, PrizeTypeNone, now)
	if err != nil {
		if isUniqueViolation(err) {
			return 0, ErrAlreadyDrawn
		}
		return 0, err
	}
	return res.LastInsertId()
}

// FinalizeLotteryDraw writes the prize outcome onto a reserved row.
func (s *Store) FinalizeLotteryDraw(ctx context.Context, id int64, label, prizeType string, amount, newBalance float64) error {
	return s.FinalizeLotteryDrawWithIndex(ctx, id, -1, label, prizeType, amount, newBalance)
}

// FinalizeLotteryDrawWithIndex stores the exact configured prize position.
func (s *Store) FinalizeLotteryDrawWithIndex(ctx context.Context, id int64, prizeIndex int, label, prizeType string, amount, newBalance float64) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE lottery_draws
SET prize_index = ?, prize_label = ?, prize_type = ?, amount = ?, new_balance = ?
WHERE id = ?
`, prizeIndex, label, prizeType, amount, newBalance, id)
	if err == nil {
		InvalidateRankingCache()
	}
	return err
}

// ReleaseLotteryDraw drops a reserved row so the user keeps the chance.
func (s *Store) ReleaseLotteryDraw(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM lottery_draws WHERE id = ?`, id)
	return err
}

// SumLotteryAmountByDate totals credited amounts for one day.
func (s *Store) SumLotteryAmountByDate(ctx context.Context, drawDate string) (float64, error) {
	var total sql.NullFloat64
	err := s.db.QueryRowContext(ctx, `
SELECT COALESCE(SUM(amount), 0) FROM lottery_draws WHERE draw_date = ?
`, drawDate).Scan(&total)
	if err != nil {
		return 0, err
	}
	return total.Float64, nil
}

// CountLotteryDrawsByDate counts draws for one day.
func (s *Store) CountLotteryDrawsByDate(ctx context.Context, drawDate string) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM lottery_draws WHERE draw_date = ?`, drawDate).Scan(&n)
	return n, err
}

// LotteryTotals is an all-time summary.
type LotteryTotals struct {
	Draws       int64   `json:"draws"`
	Winners     int64   `json:"winners"`
	TotalAmount float64 `json:"total_amount"`
}

// LotteryAllTimeTotals returns lifetime counters.
func (s *Store) LotteryAllTimeTotals(ctx context.Context) (LotteryTotals, error) {
	var out LotteryTotals
	var total sql.NullFloat64
	err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*),
       COALESCE(SUM(CASE WHEN amount > 0 THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(amount), 0)
FROM lottery_draws
`).Scan(&out.Draws, &out.Winners, &total)
	if err != nil {
		return out, err
	}
	out.TotalAmount = total.Float64
	return out, nil
}

// ListLotteryDraws returns draws newest first, optionally filtered by user.
func (s *Store) ListLotteryDraws(ctx context.Context, userID int64, limit, offset int) ([]LotteryDraw, error) {
	if limit <= 0 || limit > 200 {
		limit = 10
	}
	if offset < 0 {
		offset = 0
	}
	query := `
SELECT id, user_id, draw_date, prize_index, prize_label, prize_type, amount, new_balance, created_at
FROM lottery_draws
`
	args := []any{}
	if userID > 0 {
		query += "WHERE user_id = ?\n"
		args = append(args, userID)
	}
	query += "ORDER BY id DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LotteryDraw{}
	for rows.Next() {
		var d LotteryDraw
		var created string
		if err := rows.Scan(&d.ID, &d.UserID, &d.DrawDate, &d.PrizeIndex, &d.PrizeLabel, &d.PrizeType, &d.Amount, &d.NewBalance, &created); err != nil {
			return nil, err
		}
		if t, err := time.Parse(time.RFC3339Nano, created); err == nil {
			d.CreatedAt = t
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// CountLotteryDraws counts rows for pagination.
func (s *Store) CountLotteryDraws(ctx context.Context, userID int64) (int64, error) {
	var n int64
	var err error
	if userID > 0 {
		err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM lottery_draws WHERE user_id = ?`, userID).Scan(&n)
	} else {
		err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM lottery_draws`).Scan(&n)
	}
	return n, err
}

func scanLotteryDraw(row *sql.Row) (*LotteryDraw, error) {
	var d LotteryDraw
	var created string
	if err := row.Scan(&d.ID, &d.UserID, &d.DrawDate, &d.PrizeIndex, &d.PrizeLabel, &d.PrizeType, &d.Amount, &d.NewBalance, &created); err != nil {
		return nil, err
	}
	if t, err := time.Parse(time.RFC3339Nano, created); err == nil {
		d.CreatedAt = t
	}
	return &d, nil
}
