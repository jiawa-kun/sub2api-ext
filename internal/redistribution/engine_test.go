package redistribution

import (
	"testing"
	"time"

	"sub2api-ext/internal/sub2api"
)

func TestInactiveLogicAndPaidBalanceProtection(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	created := now.AddDate(0, 0, -120)
	lastActive := now.AddDate(0, 0, -100)
	lastUsed := now.AddDate(0, 0, -10)
	rt := DefaultRuntime()
	rt.NewUserProtectionDays = 0
	snap := UserSnapshot{User: sub2api.User{
		ID: 1, Role: "user", Status: "active", Balance: 2,
		CreatedAt: created, LastActiveAt: &lastActive, LastUsedAt: &lastUsed,
	}}

	if ok, _ := IsInactive(rt, snap, now); ok {
		t.Fatal("all logic should reject a recently used user")
	}
	rt.InactiveLogic = LogicAny
	if ok, _ := IsInactive(rt, snap, now); !ok {
		t.Fatal("any logic should match no_active_days")
	}
	snap.User.TotalRecharged = 1
	if ok, _ := IsInactive(rt, snap, now); ok {
		t.Fatal("recharged user must always be protected")
	}
}

func TestWorkdayActivitySkipsWeekendButWeekendActivityIsValid(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC) // Monday
	weekendLogin := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	snap := UserSnapshot{User: sub2api.User{ID: 1, Role: "user", Status: "active", LastActiveAt: &weekendLogin}}
	rt := DefaultRuntime()
	rt.ActiveRules = []Rule{{Type: RuleActiveWithinDays, Enabled: true, Days: 1}}
	if ok, _ := IsActive(rt, snap, now); !ok {
		t.Fatal("Saturday activity should count on Monday")
	}
	if got := addWorkdays(calendarDate(now), -1); !got.Equal(time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("workday cutoff=%v", got)
	}
}

func TestReclaimAmountModes(t *testing.T) {
	policy := ReclaimPolicy{Mode: ReclaimFixed, Value: 2, ReserveBalance: 1, MinBalance: 1, MinPerUser: 0.01, MaxPerUser: 5}
	if got := ReclaimAmount(policy, 2.5); got != 1.5 {
		t.Fatalf("fixed reclaim=%v", got)
	}
	policy.Mode = ReclaimPercent
	policy.Value = 20
	if got := ReclaimAmount(policy, 10); got != 2 {
		t.Fatalf("percent reclaim=%v", got)
	}
	policy.Mode = ReclaimExcess
	policy.MaxPerUser = 3
	if got := ReclaimAmount(policy, 10); got != 3 {
		t.Fatalf("excess reclaim=%v", got)
	}
}

func TestAllocateNeverExceedsPool(t *testing.T) {
	users := []UserSnapshot{
		{User: sub2api.User{ID: 1}, RecentUsage: 1},
		{User: sub2api.User{ID: 2}, RecentUsage: 4},
		{User: sub2api.User{ID: 3}, RecentUsage: 9},
	}
	policy := AllocationPolicy{Mode: AllocationMixed, EqualRatio: 50, MinReward: 0.0001, MaxRewardPerUser: 1, RecipientLimit: 10}
	got := Allocate(policy, 1.2345, users)
	total := 0.0
	for _, amount := range got {
		if amount > 1 {
			t.Fatalf("per-user cap exceeded: %v", amount)
		}
		total += amount
	}
	if total > 1.2345+1e-9 {
		t.Fatalf("allocation exceeds pool: %v", total)
	}
	if got[3] <= got[1] {
		t.Fatalf("higher usage should receive more: %+v", got)
	}
}

func TestDrawAmountActiveShareModes(t *testing.T) {
	users := []UserSnapshot{{User: sub2api.User{ID: 1}, RecentUsage: 1}, {User: sub2api.User{ID: 2}, RecentUsage: 3}}
	rt := DefaultRuntime()
	rt.DrawMode = DrawActiveShare
	rt.ActiveShareMode = ActiveShareEqual
	if got, err := drawAmount(rt, 1, users, 1); err != nil || got != .5 {
		t.Fatalf("equal amount=%v err=%v", got, err)
	}
	rt.ActiveShareMode = ActiveShareUsage
	if got, err := drawAmount(rt, 1, users, 2); err != nil || got != .75 {
		t.Fatalf("usage amount=%v err=%v", got, err)
	}
}
