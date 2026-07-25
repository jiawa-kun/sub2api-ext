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
