package handler

import (
	"testing"

	"sub2api-ext/internal/settings"
)

func TestIsSensitiveUpdate_OnlyIncreases(t *testing.T) {
	old := settings.Runtime{
		HardCap:      100,
		RewardAmount: 50,
		RewardMin:    1,
		RewardMax:    100,
		DailyBudget:  0,
		StreakMilestones: map[int]float64{3: 0.05, 7: 0.2},
	}
	// re-save same high values -> not sensitive
	hc, ra, rmin, rmax, db := 100.0, 50.0, 1.0, 100.0, 0.0
	in := settings.UpdateInput{
		HardCap: &hc, RewardAmount: &ra, RewardMin: &rmin, RewardMax: &rmax, DailyBudget: &db,
		StreakMilestones: map[int]float64{3: 0.05, 7: 0.2},
	}
	if isSensitiveUpdate(in, old) {
		t.Fatal("re-save same values should not be sensitive")
	}
	// raise hard cap further
	higher := 100.5
	in2 := settings.UpdateInput{HardCap: &higher}
	if !isSensitiveUpdate(in2, old) {
		t.Fatal("raising hard_cap above threshold should be sensitive")
	}
	// budget 0 not sensitive
	zero := 0.0
	if isSensitiveUpdate(settings.UpdateInput{DailyBudget: &zero}, old) {
		t.Fatal("daily_budget=0 should not be sensitive")
	}
	// budget increase
	up := 20.0
	if !isSensitiveUpdate(settings.UpdateInput{DailyBudget: &up}, old) {
		t.Fatal("raising daily_budget should be sensitive")
	}
	// lower hard cap not sensitive
	lower := 5.0
	if isSensitiveUpdate(settings.UpdateInput{HardCap: &lower}, old) {
		t.Fatal("lowering hard_cap should not be sensitive")
	}
}
