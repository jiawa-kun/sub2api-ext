package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"sub2api-ext/internal/store"
)

func TestCountStreakBeforeUsesRangeScan(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "streak.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	// today = 2026-07-30; streak ending yesterday should be 3 (27,28,29)
	for _, d := range []string{"2026-07-27", "2026-07-28", "2026-07-29"} {
		if _, err := st.TryInsert(ctx, 1, d, 0.1, 1); err != nil {
			t.Fatal(err)
		}
	}
	// gap day should not break if after streak window start... add older isolated day
	if _, err := st.TryInsert(ctx, 1, "2026-07-20", 0.1, 1); err != nil {
		t.Fatal(err)
	}
	n, err := st.CountStreakBefore(ctx, 1, "2026-07-30")
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("streak=%d want 3", n)
	}
	// no history
	n2, err := st.CountStreakBefore(ctx, 2, "2026-07-30")
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 0 {
		t.Fatalf("empty streak=%d", n2)
	}
}

func TestListTaskClaimsByPeriods(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "claims.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, err := st.InsertTaskClaim(ctx, store.TaskClaim{UserID: 9, TaskID: "daily_checkin", PeriodKey: "2026-07-30", Amount: 0.2}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertTaskClaim(ctx, store.TaskClaim{UserID: 9, TaskID: "streak_3", PeriodKey: "once", Amount: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertTaskClaim(ctx, store.TaskClaim{UserID: 9, TaskID: "old", PeriodKey: "2026-01-01", Amount: 1}); err != nil {
		t.Fatal(err)
	}
	m, err := st.ListTaskClaimsByPeriods(ctx, 9, []string{"2026-07-30", "once", "2026-07-30"})
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 {
		t.Fatalf("len=%d want 2 map=%v", len(m), m)
	}
	if _, ok := m[store.TaskClaimKey("daily_checkin", "2026-07-30")]; !ok {
		t.Fatal("missing daily")
	}
	if _, ok := m[store.TaskClaimKey("streak_3", "once")]; !ok {
		t.Fatal("missing streak")
	}
	if _, ok := m[store.TaskClaimKey("old", "2026-01-01")]; ok {
		t.Fatal("old period should be filtered out")
	}
}

func TestCountStreakBeforeInvalidDate(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "bad.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.CountStreakBefore(context.Background(), 1, "not-a-date"); err == nil {
		t.Fatal("want error")
	}
	_ = time.Now()
}
