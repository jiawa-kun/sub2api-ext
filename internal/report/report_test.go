package report

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sub2api-ext/internal/config"
	"sub2api-ext/internal/notify"
	"sub2api-ext/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "report.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestNormalizeFallsBackToSafeValues(t *testing.T) {
	rt := Normalize(Runtime{SendAt: "99:99", Timezone: "Nope/Nowhere", CoverDay: "whenever"})
	if rt.SendAt != "09:00" {
		t.Fatalf("send_at = %q, want 09:00", rt.SendAt)
	}
	if rt.Timezone != "Asia/Shanghai" {
		t.Fatalf("timezone = %q", rt.Timezone)
	}
	if rt.CoverDay != CoverYesterday {
		t.Fatalf("cover_day = %q", rt.CoverDay)
	}
	if len(rt.Sections) != len(AllSections()) {
		t.Fatalf("sections = %v", rt.Sections)
	}
}

func TestNormalizePadsSendAt(t *testing.T) {
	rt := Normalize(Runtime{SendAt: "9:5"})
	if rt.SendAt != "09:05" {
		t.Fatalf("send_at = %q, want 09:05", rt.SendAt)
	}
}

func TestValidateRejectsBadInput(t *testing.T) {
	cases := []Runtime{
		{SendAt: "", Sections: AllSections()},
		{SendAt: "24:00", Sections: AllSections()},
		{SendAt: "09:00", Timezone: "Not/AZone", Sections: AllSections()},
		{SendAt: "09:00", CoverDay: "tomorrow", Sections: AllSections()},
		{SendAt: "09:00", Sections: []string{"nope"}},
	}
	for i, rt := range cases {
		if err := Validate(rt); err == nil {
			t.Fatalf("case %d: expected error", i)
		}
	}
}

func TestCoverDate(t *testing.T) {
	now := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	if got := (Runtime{CoverDay: CoverYesterday}).CoverDate(now); got != "2026-07-26" {
		t.Fatalf("yesterday = %q", got)
	}
	if got := (Runtime{CoverDay: CoverToday}).CoverDate(now); got != "2026-07-27" {
		t.Fatalf("today = %q", got)
	}
}

func TestDueNowWindow(t *testing.T) {
	rt := Normalize(Runtime{SendAt: "09:00", Timezone: "UTC"})
	day := func(h, m int) time.Time {
		return time.Date(2026, 7, 27, h, m, 0, 0, time.UTC)
	}
	if DueNow(rt, day(8, 59)) {
		t.Fatal("must not fire before the configured time")
	}
	if !DueNow(rt, day(9, 0)) {
		t.Fatal("must fire at the configured time")
	}
	if !DueNow(rt, day(10, 59)) {
		t.Fatal("must still catch up inside the window")
	}
	if DueNow(rt, day(11, 1)) {
		t.Fatal("must not fire after the window closed")
	}
}

func TestNextDueRollsToTomorrow(t *testing.T) {
	rt := Normalize(Runtime{SendAt: "09:00", Timezone: "UTC"})
	next, ok := NextDue(rt, time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC))
	if !ok {
		t.Fatal("next due not computed")
	}
	if got := next.Format("2006-01-02 15:04"); got != "2026-07-28 09:00" {
		t.Fatalf("next = %q", got)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	st := newTestStore(t)
	s := NewSettings(st, config.ReportConfig{})
	enabled := true
	sendAt := "23:30"
	cover := CoverToday
	sections := []string{SectionCheckin}
	rt, err := s.Update(context.Background(), UpdateInput{
		Enabled:  &enabled,
		SendAt:   &sendAt,
		CoverDay: &cover,
		Sections: &sections,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !rt.Enabled || rt.SendAt != "23:30" || rt.CoverDay != CoverToday {
		t.Fatalf("unexpected runtime %+v", rt)
	}
	// A fresh Settings must read the same values back from SQLite.
	again := NewSettings(st, config.ReportConfig{})
	got := again.Get()
	if !got.Enabled || got.SendAt != "23:30" || got.CoverDay != CoverToday {
		t.Fatalf("reload lost values: %+v", got)
	}
	if len(got.Sections) != 1 || got.Sections[0] != SectionCheckin {
		t.Fatalf("sections = %v", got.Sections)
	}
}

func TestUpdateRejectsEmptySections(t *testing.T) {
	st := newTestStore(t)
	s := NewSettings(st, config.ReportConfig{})
	empty := []string{}
	if _, err := s.Update(context.Background(), UpdateInput{Sections: &empty}); err == nil {
		t.Fatal("expected error for empty sections")
	}
}

func seedCheckin(t *testing.T, st *store.Store, userID int64, date string, amount float64) {
	t.Helper()
	if _, err := st.TryInsert(context.Background(), userID, date, amount, 0); err != nil {
		t.Fatalf("seed checkin: %v", err)
	}
}

func seedDraw(t *testing.T, st *store.Store, userID int64, date, label string, amount float64) {
	t.Helper()
	ctx := context.Background()
	id, err := st.ReserveLotteryDraw(ctx, userID, date)
	if err != nil {
		t.Fatalf("reserve draw: %v", err)
	}
	kind := store.PrizeTypeNone
	if amount > 0 {
		kind = store.PrizeTypeBalance
	}
	if err := st.FinalizeLotteryDraw(ctx, id, label, kind, amount, 0); err != nil {
		t.Fatalf("finalize draw: %v", err)
	}
}

func TestBuildAggregatesCheckinAndLottery(t *testing.T) {
	st := newTestStore(t)
	seedCheckin(t, st, 1, "2026-07-26", 0.5)
	seedCheckin(t, st, 2, "2026-07-26", 0.5)
	seedCheckin(t, st, 3, "2026-07-25", 0.5)
	seedDraw(t, st, 1, "2026-07-26", "1 额度", 1)
	seedDraw(t, st, 2, "2026-07-26", "谢谢参与", 0)

	rt := Normalize(Runtime{SendAt: "09:00", Timezone: "UTC", Sections: []string{SectionCheckin, SectionLottery}})
	deps := Deps{
		CheckinBudget:  func() float64 { return 10 },
		LotteryBudget:  func() float64 { return 0 },
		LotteryEnabled: func() bool { return true },
	}
	d, err := Build(context.Background(), st, rt, deps, "2026-07-26", time.Now())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if d.Checkin == nil || d.Checkin.Count != 2 {
		t.Fatalf("checkin block = %+v", d.Checkin)
	}
	if d.Checkin.PrevCount != 1 {
		t.Fatalf("prev count = %d, want 1", d.Checkin.PrevCount)
	}
	if d.Lottery == nil || d.Lottery.Draws != 2 || d.Lottery.Winners != 1 {
		t.Fatalf("lottery block = %+v", d.Lottery)
	}
	if d.Lottery.Amount != 1 {
		t.Fatalf("lottery amount = %v", d.Lottery.Amount)
	}
	// Patrol was not selected, so it must be absent entirely.
	if d.Patrol != nil {
		t.Fatalf("patrol block should be nil, got %+v", d.Patrol)
	}
	text := d.PlainText()
	for _, want := range []string{"签到人数", "抽奖次数", "2026-07-26"} {
		if !strings.Contains(text, want) {
			t.Fatalf("rendered text missing %q:\n%s", want, text)
		}
	}
}

func TestBuildBudgetExhaustedRaisesLevel(t *testing.T) {
	st := newTestStore(t)
	seedCheckin(t, st, 1, "2026-07-26", 5)
	rt := Normalize(Runtime{SendAt: "09:00", Timezone: "UTC", Sections: []string{SectionCheckin}})
	deps := Deps{CheckinBudget: func() float64 { return 5 }}
	d, err := Build(context.Background(), st, rt, deps, "2026-07-26", time.Now())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if d.Level != notify.LevelWarn {
		t.Fatalf("level = %q, want warn", d.Level)
	}
}

func TestBuildPatrolCountsOnlyTargetDay(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	mk := func(local time.Time, status string, failed, disabled int) {
		stats, _ := json.Marshal(patrolStats{Checked: 10, OK: 10 - failed, Failed: failed, Disabled: disabled})
		id, err := st.InsertPatrolRun(ctx, store.PatrolRun{TriggerType: "cron", Status: "running", StartedAt: local.UTC()})
		if err != nil {
			t.Fatalf("insert run: %v", err)
		}
		if err := st.UpdatePatrolRun(ctx, id, status, string(stats), "[]", "", local.Add(time.Minute).UTC()); err != nil {
			t.Fatalf("update run: %v", err)
		}
	}
	// 00:30 local on the target day is 16:30 UTC on the previous day: the
	// aggregation must group by local date, not by UTC date.
	mk(time.Date(2026, 7, 26, 0, 30, 0, 0, loc), "success", 1, 1)
	mk(time.Date(2026, 7, 26, 23, 30, 0, 0, loc), "success", 0, 0)
	mk(time.Date(2026, 7, 27, 0, 30, 0, 0, loc), "success", 5, 5)

	rt := Normalize(Runtime{SendAt: "09:00", Timezone: "Asia/Shanghai", Sections: []string{SectionPatrol}})
	d, err := Build(ctx, st, rt, Deps{}, "2026-07-26", time.Now())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if d.Patrol == nil {
		t.Fatal("patrol block missing")
	}
	if d.Patrol.Runs != 2 {
		t.Fatalf("runs = %d, want 2", d.Patrol.Runs)
	}
	if d.Patrol.FailedAcct != 1 || d.Patrol.Disabled != 1 {
		t.Fatalf("patrol block = %+v", d.Patrol)
	}
}

func TestBuildPatrolNoRuns(t *testing.T) {
	st := newTestStore(t)
	rt := Normalize(Runtime{SendAt: "09:00", Timezone: "UTC", Sections: []string{SectionPatrol}})
	d, err := Build(context.Background(), st, rt, Deps{}, "2026-07-26", time.Now())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(d.PlainText(), "当日没有巡检运行") {
		t.Fatalf("unexpected text:\n%s", d.PlainText())
	}
}

func TestSendNowFailsWhenNotifyDisabled(t *testing.T) {
	st := newTestStore(t)
	settings := NewSettings(st, config.ReportConfig{})
	nset := notify.NewSettings(st, config.NotifyConfig{})
	svc := NewService(st, settings, notify.NewNotifier(nset), Deps{})
	if _, err := svc.SendNow(context.Background(), time.Now()); err == nil {
		t.Fatal("expected an error when the notify channel is off")
	}
	if got := svc.Stats().Failed; got != 1 {
		t.Fatalf("failed counter = %d, want 1", got)
	}
}

func TestEventTypeIsNotSubscribable(t *testing.T) {
	// The digest is delivered directly, so it must not show up as an alert
	// subscription checkbox in the notify module.
	for _, tpe := range notify.AllTypes() {
		if tpe == notify.TypeDailyReport {
			t.Fatal("report type must stay out of the subscription list")
		}
	}
	if notify.TypeLabel(notify.TypeDailyReport) == notify.TypeDailyReport {
		t.Fatal("report type needs a human label")
	}
}
