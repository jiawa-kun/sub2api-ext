package tasks_test

import (
	"testing"
	"time"

	"sub2api-ext/internal/tasks"
)

func TestPeriodKeyAndWeekRange(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	// Wednesday
	now := time.Date(2026, 7, 29, 15, 0, 0, 0, loc)
	daily := tasks.Def{Period: "daily"}
	if got := tasks.PeriodKey(daily, loc, now); got != "2026-07-29" {
		t.Fatalf("daily=%s", got)
	}
	weekly := tasks.Def{Period: "weekly"}
	gotW := tasks.PeriodKey(weekly, loc, now)
	if len(gotW) < 7 || gotW[4] != '-' || gotW[5] != 'W' {
		t.Fatalf("weekly key=%s", gotW)
	}
	if tasks.PeriodKey(tasks.Def{Period: "once"}, loc, now) != "once" {
		t.Fatal("once")
	}
	from, to := tasks.WeekRange(loc, now)
	// Mon 2026-07-27 .. Sun 2026-08-02
	if from != "2026-07-27" || to != "2026-08-02" {
		t.Fatalf("week=%s..%s", from, to)
	}
}
