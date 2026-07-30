package patrol

import (
	"testing"
	"time"
)

func TestParseAndMatchCron(t *testing.T) {
	expr, err := ParseCron("0 */6 * * *")
	if err != nil {
		t.Fatal(err)
	}
	loc := time.FixedZone("CST", 8*3600)
	// 00:00 matches
	tm := time.Date(2026, 7, 25, 0, 0, 10, 0, loc)
	if !expr.Matches(tm) {
		t.Fatalf("expected match at %v", tm)
	}
	// 00:01 no
	tm2 := time.Date(2026, 7, 25, 0, 1, 0, 0, loc)
	if expr.Matches(tm2) {
		t.Fatalf("unexpected match at %v", tm2)
	}
	// 06:00 matches
	tm3 := time.Date(2026, 7, 25, 6, 0, 0, 0, loc)
	if !expr.Matches(tm3) {
		t.Fatalf("expected match at %v", tm3)
	}
}

func TestParseCronInvalid(t *testing.T) {
	if _, err := ParseCron("* * *"); err == nil {
		t.Fatal("expected error")
	}
}

func TestCronNext(t *testing.T) {
	expr, err := ParseCron("*/5 * * * *")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 30, 10, 2, 30, 0, time.UTC)
	next := expr.Next(base)
	want := time.Date(2026, 7, 30, 10, 5, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("Next(%v)=%v want %v", base, next, want)
	}
	// Strictly after an exact match minute.
	exact := time.Date(2026, 7, 30, 10, 5, 0, 0, time.UTC)
	next2 := expr.Next(exact)
	want2 := time.Date(2026, 7, 30, 10, 10, 0, 0, time.UTC)
	if !next2.Equal(want2) {
		t.Fatalf("Next(exact)=%v want %v", next2, want2)
	}
}
