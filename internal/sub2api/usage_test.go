package sub2api_test

import (
	"testing"
	"time"

	"sub2api-ext/internal/sub2api"
)

func TestResolveRange(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 30, 15, 0, 0, 0, loc)
	from, to, p := sub2api.ResolveRange("today", now, loc)
	if from != "2026-07-30" || to != "2026-07-30" || p != "today" {
		t.Fatalf("today %s %s %s", from, to, p)
	}
	from, to, p = sub2api.ResolveRange("yesterday", now, loc)
	if from != "2026-07-29" || to != "2026-07-29" || p != "yesterday" {
		t.Fatalf("yesterday %s %s %s", from, to, p)
	}
	from, to, p = sub2api.ResolveRange("7d", now, loc)
	if from != "2026-07-24" || to != "2026-07-30" || p != "7d" {
		t.Fatalf("7d %s %s %s", from, to, p)
	}
	from, to, p = sub2api.ResolveRange("30d", now, loc)
	if from != "2026-07-01" || to != "2026-07-30" || p != "30d" {
		t.Fatalf("30d %s %s %s", from, to, p)
	}
}
