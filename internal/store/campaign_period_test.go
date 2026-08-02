package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
	"sub2api-ext/internal/store"
)

func TestPreviousCampaignPeriodsShanghai(t *testing.T) {
	loc, err := time.LoadLocation(store.CampaignTimezone)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 3, 3, 0, 0, 0, loc) // Monday
	daily, err := store.PreviousCampaignPeriod(store.CampaignFrequencyDaily, now, loc)
	if err != nil || daily.Key != "2026-08-02" || daily.StartDate != daily.EndDate {
		t.Fatalf("daily=%+v err=%v", daily, err)
	}
	weekly, err := store.PreviousCampaignPeriod(store.CampaignFrequencyWeekly, now, loc)
	if err != nil || weekly.Key != "2026-W31" || weekly.StartDate != "2026-07-27" || weekly.EndDate != "2026-08-02" {
		t.Fatalf("weekly=%+v err=%v", weekly, err)
	}
	monthly, err := store.PreviousCampaignPeriod(store.CampaignFrequencyMonthly, now, loc)
	if err != nil || monthly.Key != "2026-07" || monthly.StartDate != "2026-07-01" || monthly.EndDate != "2026-07-31" {
		t.Fatalf("monthly=%+v err=%v", monthly, err)
	}
	parsed, err := store.CampaignPeriodFromKey(store.CampaignFrequencyWeekly, "2026-W31", loc)
	if err != nil || parsed.StartDate != weekly.StartDate || parsed.EndDate != weekly.EndDate {
		t.Fatalf("parsed=%+v err=%v", parsed, err)
	}
}

func TestCampaignAwardMigrationAddsPeriodUniqueness(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE rank_campaign_awards (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  campaign_id INTEGER NOT NULL,
  user_id INTEGER NOT NULL,
  rank INTEGER NOT NULL,
  amount REAL NOT NULL DEFAULT 0,
  ledger_id INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'success',
  created_at TEXT NOT NULL,
  UNIQUE(campaign_id, user_id)
);
INSERT INTO rank_campaign_awards(campaign_id,user_id,rank,amount,status,created_at)
VALUES(1,7,1,5,'success','2026-08-01T00:00:00Z');
`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	awards, err := st.ListCampaignAwards(ctx, 1)
	if err != nil || len(awards) != 1 || awards[0].PeriodKey != store.CampaignFrequencyOnce {
		t.Fatalf("migrated awards=%+v err=%v", awards, err)
	}
	if _, err := st.InsertCampaignAward(ctx, store.RankCampaignAward{
		CampaignID: 1, PeriodKey: "2026-08-02", UserID: 7, Rank: 1, Amount: 5, Status: "success",
	}); err != nil {
		t.Fatalf("same user should be eligible in another period: %v", err)
	}
}
