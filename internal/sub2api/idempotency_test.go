package sub2api

import "testing"

// The check-in idempotency key format is a compatibility contract with the
// upstream: keys already issued as "checkin-<id>-<date>" must keep matching,
// otherwise a re-credit would be treated as a fresh grant.
func TestIdempotencyKeyCheckinFormatUnchanged(t *testing.T) {
	got := IdempotencyKey(IdempotencyScopeCheckin, 42, "2026-07-27")
	if want := "checkin-42-2026-07-27"; got != want {
		t.Fatalf("checkin key drifted: got %q want %q", got, want)
	}
}

func TestIdempotencyKeyScopesDoNotCollide(t *testing.T) {
	checkin := IdempotencyKey(IdempotencyScopeCheckin, 7, "2026-07-27")
	lottery := IdempotencyKey(IdempotencyScopeLottery, 7, "2026-07-27")
	if checkin == lottery {
		t.Fatalf("same key for different scopes: %q", checkin)
	}
	if want := "lottery-7-2026-07-27"; lottery != want {
		t.Fatalf("lottery key: got %q want %q", lottery, want)
	}
}

func TestIdempotencyKeySanitizes(t *testing.T) {
	cases := []struct {
		name  string
		scope string
		slot  string
		want  string
	}{
		{"empty slot falls back", IdempotencyScopeLottery, "", "lottery-1-unknown"},
		{"empty scope falls back to checkin", "", "2026-07-27", "checkin-1-2026-07-27"},
		{"spaces and colons become dashes", IdempotencyScopeLottery, "2026-07-27 10:00", "lottery-1-2026-07-27-10-00"},
		{"non-ascii dropped", IdempotencyScopeLottery, "日期2026", "lottery-1-2026"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IdempotencyKey(tc.scope, 1, tc.slot); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestIdempotencyKeyHasNoSpacesAndFitsLimit(t *testing.T) {
	long := ""
	for i := 0; i < 300; i++ {
		long += "x"
	}
	got := IdempotencyKey(IdempotencyScopeLottery, 999999, long)
	if len(got) > 128 {
		t.Fatalf("key too long for upstream: %d", len(got))
	}
	for _, r := range got {
		if r <= 32 || r > 126 {
			t.Fatalf("key has non-printable or space char %q in %q", r, got)
		}
	}
}
