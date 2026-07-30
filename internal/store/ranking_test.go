package store_test

import (
	"context"
	"path/filepath"
	"testing"

	"sub2api-ext/internal/store"
)

func seedLottery(t *testing.T, st *store.Store, userID int64, date, label, ptype string, amount, bal float64) {
	t.Helper()
	id, err := st.ReserveLotteryDraw(context.Background(), userID, date)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FinalizeLotteryDraw(context.Background(), id, label, ptype, amount, bal); err != nil {
		t.Fatal(err)
	}
}

func TestListRewardRankingAggregatesCheckinAndLottery(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	if _, err := st.TryInsert(ctx, 1, "2026-07-30", 0.10, 1.0); err != nil {
		t.Fatal(err)
	}
	if _, err := st.TryInsert(ctx, 2, "2026-07-30", 0.20, 2.0); err != nil {
		t.Fatal(err)
	}
	if _, err := st.TryInsert(ctx, 1, "2026-07-29", 0.10, 0.9); err != nil {
		t.Fatal(err)
	}
	seedLottery(t, st, 1, "2026-07-30", "小奖", store.PrizeTypeBalance, 0.5, 1.5)
	seedLottery(t, st, 2, "2026-07-30", "谢谢", store.PrizeTypeNone, 0, 2.0)

	rows, summary, err := st.ListRewardRanking(ctx, "2026-07-30", "2026-07-30", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	if rows[0].UserID != 1 || rows[0].TotalAmount < 0.59 || rows[0].TotalAmount > 0.61 {
		t.Fatalf("unexpected top row: %+v", rows[0])
	}
	if rows[0].CheckinAmount < 0.09 || rows[0].LotteryAmount < 0.49 {
		t.Fatalf("unexpected split: %+v", rows[0])
	}
	if summary.TotalAmount < 0.79 || summary.TotalAmount > 0.81 {
		t.Fatalf("summary total=%v", summary.TotalAmount)
	}
	if summary.TopAmount < 0.59 {
		t.Fatalf("top=%v", summary.TopAmount)
	}

	rank, amt, err := st.RewardRankOfUser(ctx, "2026-07-30", "2026-07-30", 2)
	if err != nil {
		t.Fatal(err)
	}
	if rank != 2 || amt < 0.19 {
		t.Fatalf("user2 rank=%d amt=%v", rank, amt)
	}

	rows7, sum7, err := st.ListRewardRanking(ctx, "2026-07-29", "2026-07-30", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows7) != 2 {
		t.Fatalf("7d rows=%d", len(rows7))
	}
	if rows7[0].UserID != 1 || rows7[0].TotalAmount < 0.69 {
		t.Fatalf("multi-day top=%+v", rows7[0])
	}
	if sum7.UserCount != 2 {
		t.Fatalf("user count=%d", sum7.UserCount)
	}
}
