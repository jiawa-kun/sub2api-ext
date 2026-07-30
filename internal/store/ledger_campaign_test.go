package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"sub2api-ext/internal/store"
	"sub2api-ext/internal/tasks"
)

func TestAmountForRankExactAndRange(t *testing.T) {
	rules := []store.RankRewardRule{
		{Rank: 1, Amount: 5},
		{RankFrom: 2, RankTo: 5, Amount: 1},
		{RankFrom: 6, RankTo: 10, Amount: 0.5},
	}
	if got := store.AmountForRank(rules, 1); got != 5 {
		t.Fatalf("rank1=%v", got)
	}
	if got := store.AmountForRank(rules, 3); got != 1 {
		t.Fatalf("rank3=%v", got)
	}
	if got := store.AmountForRank(rules, 8); got != 0.5 {
		t.Fatalf("rank8=%v", got)
	}
	if got := store.AmountForRank(rules, 11); got != 0 {
		t.Fatalf("rank11=%v", got)
	}
	// exact wins over range
	rules2 := []store.RankRewardRule{
		{Rank: 3, Amount: 9},
		{RankFrom: 2, RankTo: 5, Amount: 1},
	}
	if got := store.AmountForRank(rules2, 3); got != 9 {
		t.Fatalf("exact override=%v", got)
	}
}

func TestLedgerInsertAndIdem(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	id, err := st.InsertLedger(ctx, store.LedgerEntry{
		UserID: 7, Source: store.LedgerSourceCheckin, SourceRef: "d1",
		Amount: 0.1, IdempotencyKey: "checkin-7-2026-07-30", Status: store.LedgerStatusSuccess,
	})
	if err != nil || id <= 0 {
		t.Fatalf("insert id=%d err=%v", id, err)
	}
	ok, err := st.HasLedgerIdem(ctx, "checkin-7-2026-07-30")
	if err != nil || !ok {
		t.Fatalf("has idem ok=%v err=%v", ok, err)
	}
	_, err = st.InsertLedger(ctx, store.LedgerEntry{
		UserID: 7, Source: store.LedgerSourceCheckin, Amount: 0.1,
		IdempotencyKey: "checkin-7-2026-07-30", Status: store.LedgerStatusSuccess,
	})
	if err == nil {
		t.Fatal("expected duplicate idempotency error")
	}
	list, err := st.ListLedger(ctx, store.LedgerFilter{Source: store.LedgerSourceCheckin, Limit: 10})
	if err != nil || len(list) != 1 {
		t.Fatalf("list=%d err=%v", len(list), err)
	}
}

func TestLedgerBackfillFromLegacy(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, err := st.TryInsert(ctx, 1, "2026-07-30", 0.12, 1.0); err != nil {
		t.Fatal(err)
	}
	id, err := st.ReserveLotteryDraw(ctx, 1, "2026-07-30")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FinalizeLotteryDraw(ctx, id, "奖", store.PrizeTypeBalance, 0.3, 1.3); err != nil {
		t.Fatal(err)
	}
	n, err := st.BackfillLedgerFromLegacy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n < 2 {
		t.Fatalf("backfill inserted %d, want >=2", n)
	}
	// second backfill should insert 0 new success rows
	n2, err := st.BackfillLedgerFromLegacy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 0 {
		t.Fatalf("second backfill=%d", n2)
	}
}

func TestCampaignCRUDAndAwards(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	id, err := st.CreateRankCampaign(ctx, store.RankCampaign{
		Name: "七月", Board: store.CampaignBoardRewards,
		StartDate: "2026-07-01", EndDate: "2026-07-31",
		TopN: 10, RewardsJSON: `[{"rank":1,"amount":5}]`, Status: store.CampaignStatusActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.GetRankCampaign(ctx, id)
	if err != nil || c == nil || c.Name != "七月" {
		t.Fatalf("get=%+v err=%v", c, err)
	}
	active, err := st.ListActiveRankCampaigns(ctx, "2026-07-15")
	if err != nil || len(active) != 1 {
		t.Fatalf("active=%d err=%v", len(active), err)
	}
	if _, err := st.InsertCampaignAward(ctx, store.RankCampaignAward{
		CampaignID: id, UserID: 9, Rank: 1, Amount: 5, Status: "success",
	}); err != nil {
		t.Fatal(err)
	}
	awards, err := st.ListCampaignAwards(ctx, id)
	if err != nil || len(awards) != 1 || awards[0].UserID != 9 {
		t.Fatalf("awards=%+v err=%v", awards, err)
	}
	if err := st.MarkCampaignSettled(ctx, id); err != nil {
		t.Fatal(err)
	}
	c2, _ := st.GetRankCampaign(ctx, id)
	if c2.Status != store.CampaignStatusSettled {
		t.Fatalf("status=%s", c2.Status)
	}
}

func TestTaskClaimUnique(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, err := st.InsertTaskClaim(ctx, store.TaskClaim{
		UserID: 3, TaskID: "daily_checkin", PeriodKey: "2026-07-30", Amount: 0.2,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertTaskClaim(ctx, store.TaskClaim{
		UserID: 3, TaskID: "daily_checkin", PeriodKey: "2026-07-30", Amount: 0.2,
	}); err == nil {
		t.Fatal("expected already claimed")
	}
	c, err := st.GetTaskClaim(ctx, 3, "daily_checkin", "2026-07-30")
	if err != nil || c == nil {
		t.Fatalf("get claim=%v err=%v", c, err)
	}
}

func TestTaskPeriodKeyAndWeekRange(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, loc) // Thursday
	d := tasks.Def{Period: "daily"}
	if k := tasks.PeriodKey(d, loc, now); k != "2026-07-30" {
		t.Fatalf("daily key=%s", k)
	}
	d.Period = "weekly"
	if k := tasks.PeriodKey(d, loc, now); k != "2026-W31" {
		t.Fatalf("weekly key=%s", k)
	}
	d.Period = "once"
	if k := tasks.PeriodKey(d, loc, now); k != "once" {
		t.Fatalf("once key=%s", k)
	}
	from, to := tasks.WeekRange(loc, now)
	if from != "2026-07-27" || to != "2026-08-02" {
		t.Fatalf("week=%s..%s", from, to)
	}
}


func TestListActiveRankCampaignsDateWindow(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	id, err := st.CreateRankCampaign(ctx, store.RankCampaign{
		Name: "窗", Board: store.CampaignBoardRewards,
		StartDate: "2026-07-01", EndDate: "2026-07-10",
		TopN: 3, RewardsJSON: `[{"rank":1,"amount":1}]`, Status: store.CampaignStatusActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = id
	in, err := st.ListActiveRankCampaigns(ctx, "2026-07-05")
	if err != nil || len(in) != 1 {
		t.Fatalf("in window=%d err=%v", len(in), err)
	}
	out, err := st.ListActiveRankCampaigns(ctx, "2026-07-20")
	if err != nil || len(out) != 0 {
		t.Fatalf("out window=%d err=%v", len(out), err)
	}
}

func TestMarkCampaignPartialAndUpdateAward(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	id, err := st.CreateRankCampaign(ctx, store.RankCampaign{
		Name: "p", Board: store.CampaignBoardRewards,
		StartDate: "2026-07-01", EndDate: "2026-07-31",
		Status: store.CampaignStatusActive, RewardsJSON: `[]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	aid, err := st.InsertCampaignAward(ctx, store.RankCampaignAward{
		CampaignID: id, UserID: 1, Rank: 1, Amount: 5, Status: "failed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateCampaignAward(ctx, aid, 5, 99, "success"); err != nil {
		t.Fatal(err)
	}
	m, err := st.CampaignAwardMap(ctx, id)
	if err != nil || m[1].Status != "success" || m[1].LedgerID != 99 {
		t.Fatalf("map=%+v err=%v", m, err)
	}
	if err := st.MarkCampaignStatus(ctx, id, store.CampaignStatusPartial, false); err != nil {
		t.Fatal(err)
	}
	c, _ := st.GetRankCampaign(ctx, id)
	if c.Status != store.CampaignStatusPartial {
		t.Fatalf("status=%s", c.Status)
	}
}


func TestCancelCampaignStatus(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	id, err := st.CreateRankCampaign(ctx, store.RankCampaign{
		Name: "to-cancel", Board: store.CampaignBoardRewards,
		StartDate: "2026-07-01", EndDate: "2026-07-31",
		Status: store.CampaignStatusActive, RewardsJSON: `[]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MarkCampaignStatus(ctx, id, store.CampaignStatusCancelled, false); err != nil {
		t.Fatal(err)
	}
	c, err := st.GetRankCampaign(ctx, id)
	if err != nil || c == nil || c.Status != store.CampaignStatusCancelled {
		t.Fatalf("got=%+v err=%v", c, err)
	}
	// cancelled must not appear as active in window
	active, err := st.ListActiveRankCampaigns(ctx, "2026-07-15")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range active {
		if a.ID == id {
			t.Fatal("cancelled campaign still active")
		}
	}
}
