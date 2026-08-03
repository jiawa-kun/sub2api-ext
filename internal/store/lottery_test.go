package store_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"sub2api-ext/internal/store"

	_ "modernc.org/sqlite"
)

func openLotteryStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "lottery.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestReserveLotteryDrawIsOncePerUserPerDay(t *testing.T) {
	st := openLotteryStore(t)
	ctx := context.Background()

	id, err := st.ReserveLotteryDraw(ctx, 1, "2026-07-27")
	if err != nil {
		t.Fatal(err)
	}
	if id <= 0 {
		t.Fatalf("bad id %d", id)
	}
	if _, err := st.ReserveLotteryDraw(ctx, 1, "2026-07-27"); !errors.Is(err, store.ErrAlreadyDrawn) {
		t.Fatalf("second reserve err = %v, want ErrAlreadyDrawn", err)
	}
	// A different day is allowed again.
	if _, err := st.ReserveLotteryDraw(ctx, 1, "2026-07-28"); err != nil {
		t.Fatalf("next day should be allowed: %v", err)
	}
	// A different user is independent.
	if _, err := st.ReserveLotteryDraw(ctx, 2, "2026-07-27"); err != nil {
		t.Fatalf("other user should be allowed: %v", err)
	}
}

// The UNIQUE index, not the handler pre-check, is what must stop a double
// grant when two requests race.
func TestReserveLotteryDrawConcurrentOnlyOneWins(t *testing.T) {
	st := openLotteryStore(t)
	ctx := context.Background()

	const n = 12
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, dup := 0, 0
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, err := st.ReserveLotteryDraw(ctx, 99, "2026-07-27")
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, store.ErrAlreadyDrawn):
				dup++
			default:
				t.Errorf("unexpected err: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok != 1 {
		t.Fatalf("winners = %d, want exactly 1", ok)
	}
	if dup != n-1 {
		t.Fatalf("duplicates = %d, want %d", dup, n-1)
	}
}

func TestReleaseLotteryDrawGivesTheChanceBack(t *testing.T) {
	st := openLotteryStore(t)
	ctx := context.Background()

	id, err := st.ReserveLotteryDraw(ctx, 5, "2026-07-27")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ReleaseLotteryDraw(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReserveLotteryDraw(ctx, 5, "2026-07-27"); err != nil {
		t.Fatalf("after release the user must be able to draw again: %v", err)
	}
}

func TestFinalizeAndGetLotteryDraw(t *testing.T) {
	st := openLotteryStore(t)
	ctx := context.Background()

	id, err := st.ReserveLotteryDraw(ctx, 3, "2026-07-27")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FinalizeLotteryDraw(ctx, id, "5 额度", store.PrizeTypeBalance, 5, 105); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetLotteryDraw(ctx, 3, "2026-07-27")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("draw not found")
	}
	if got.PrizeLabel != "5 额度" || got.Amount != 5 || got.NewBalance != 105 || got.PrizeType != store.PrizeTypeBalance {
		t.Fatalf("unexpected row: %+v", got)
	}
	if got.CreatedAt.IsZero() {
		t.Fatal("created_at not parsed")
	}
}

func TestGetLotteryDrawMissingReturnsNil(t *testing.T) {
	st := openLotteryStore(t)
	got, err := st.GetLotteryDraw(context.Background(), 404, "2026-07-27")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestOpenMigratesLegacyLotteryDrawsPrizeIndex(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE lottery_draws (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  draw_date TEXT NOT NULL,
  prize_label TEXT NOT NULL DEFAULT '',
  prize_type TEXT NOT NULL DEFAULT 'none',
  amount REAL NOT NULL DEFAULT 0,
  new_balance REAL NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL
);
CREATE UNIQUE INDEX idx_lottery_user_date ON lottery_draws(user_id, draw_date);
INSERT INTO lottery_draws(user_id, draw_date, prize_label, prize_type, amount, new_balance, created_at)
VALUES(11, '2026-07-27', '旧记录', 'balance', 3, 103, '2026-07-27T00:00:00Z');
`)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	got, err := st.GetLotteryDraw(context.Background(), 11, "2026-07-27")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.PrizeIndex != -1 || got.PrizeLabel != "旧记录" || got.Amount != 3 {
		t.Fatalf("legacy draw migration/read failed: %+v", got)
	}
}

func TestLotteryDailySumAndCount(t *testing.T) {
	st := openLotteryStore(t)
	ctx := context.Background()

	seed := func(user int64, date, label string, amount float64) {
		id, err := st.ReserveLotteryDraw(ctx, user, date)
		if err != nil {
			t.Fatal(err)
		}
		typ := store.PrizeTypeBalance
		if amount == 0 {
			typ = store.PrizeTypeNone
		}
		if err := st.FinalizeLotteryDraw(ctx, id, label, typ, amount, 0); err != nil {
			t.Fatal(err)
		}
	}
	seed(1, "2026-07-27", "1 额度", 1)
	seed(2, "2026-07-27", "谢谢参与", 0)
	seed(3, "2026-07-27", "5 额度", 5)
	seed(4, "2026-07-28", "10 额度", 10)

	sum, err := st.SumLotteryAmountByDate(ctx, "2026-07-27")
	if err != nil {
		t.Fatal(err)
	}
	if sum != 6 {
		t.Fatalf("sum = %v, want 6", sum)
	}
	count, err := st.CountLotteryDrawsByDate(ctx, "2026-07-27")
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("count = %d, want 3", count)
	}

	totals, err := st.LotteryAllTimeTotals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if totals.Draws != 4 || totals.Winners != 3 || totals.TotalAmount != 16 {
		t.Fatalf("totals = %+v", totals)
	}
}

func TestSumLotteryAmountEmptyDayIsZero(t *testing.T) {
	st := openLotteryStore(t)
	sum, err := st.SumLotteryAmountByDate(context.Background(), "2099-01-01")
	if err != nil {
		t.Fatal(err)
	}
	if sum != 0 {
		t.Fatalf("sum = %v, want 0", sum)
	}
}

func TestListLotteryDrawsPagingAndFilter(t *testing.T) {
	st := openLotteryStore(t)
	ctx := context.Background()
	for i := 1; i <= 5; i++ {
		id, err := st.ReserveLotteryDraw(ctx, int64(i%2+1), "2026-07-"+pad(i))
		if err != nil {
			t.Fatal(err)
		}
		if err := st.FinalizeLotteryDraw(ctx, id, "p", store.PrizeTypeBalance, float64(i), 0); err != nil {
			t.Fatal(err)
		}
	}
	all, err := st.ListLotteryDraws(ctx, 0, 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("limit ignored: %d", len(all))
	}
	// newest first
	if all[0].ID < all[1].ID {
		t.Fatal("expected DESC order by id")
	}
	filtered, err := st.ListLotteryDraws(ctx, 1, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range filtered {
		if d.UserID != 1 {
			t.Fatalf("filter leaked user %d", d.UserID)
		}
	}
	total, err := st.CountLotteryDraws(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if int(total) != len(filtered) {
		t.Fatalf("count %d vs list %d", total, len(filtered))
	}
}

func pad(i int) string {
	if i < 10 {
		return "0" + string(rune('0'+i))
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}
