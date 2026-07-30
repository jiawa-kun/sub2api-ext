package store_test

import (
	"context"
	"path/filepath"
	"testing"

	"sub2api-ext/internal/store"
)

func TestStatsByDatesAndLotteryBatch(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "rep.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, err := st.TryInsert(ctx, 1, "2026-07-29", 0.1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := st.TryInsert(ctx, 2, "2026-07-30", 0.2, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := st.TryInsert(ctx, 3, "2026-07-30", 0.3, 1); err != nil {
		t.Fatal(err)
	}
	m, err := st.StatsByDates(ctx, []string{"2026-07-29", "2026-07-30", "2026-07-28"})
	if err != nil {
		t.Fatal(err)
	}
	if m["2026-07-29"].Count != 1 || m["2026-07-30"].Count != 2 {
		t.Fatalf("%+v", m)
	}
	if m["2026-07-28"].Count != 0 {
		t.Fatalf("empty day should exist with zero: %+v", m["2026-07-28"])
	}

	id, err := st.ReserveLotteryDraw(ctx, 1, "2026-07-30")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FinalizeLotteryDraw(ctx, id, "x", store.PrizeTypeBalance, 1.5, 2); err != nil {
		t.Fatal(err)
	}
	lm, err := st.LotteryStatsByDates(ctx, []string{"2026-07-30", "2026-07-29"})
	if err != nil {
		t.Fatal(err)
	}
	if lm["2026-07-30"].Draws != 1 || lm["2026-07-30"].TotalAmount < 1.4 {
		t.Fatalf("%+v", lm["2026-07-30"])
	}
	if lm["2026-07-29"].Draws != 0 {
		t.Fatalf("prev=%+v", lm["2026-07-29"])
	}
}
