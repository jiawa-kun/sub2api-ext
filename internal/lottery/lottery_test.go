package lottery

import (
	"math"
	"testing"
)

func TestPickRespectsWeights(t *testing.T) {
	prizes := []Prize{
		{Label: "a", Amount: 0, Weight: 1},
		{Label: "b", Amount: 1, Weight: 2},
		{Label: "c", Amount: 5, Weight: 7},
	}
	// Drive every possible roll once: the label distribution must match the
	// configured weights exactly.
	counts := map[string]int{}
	for roll := int64(0); roll < 10; roll++ {
		r := roll
		p := Pick(prizes, func(int64) int64 { return r })
		counts[p.Label]++
	}
	if counts["a"] != 1 || counts["b"] != 2 || counts["c"] != 7 {
		t.Fatalf("weight mapping wrong: %v", counts)
	}
}

func TestPickSkipsZeroWeight(t *testing.T) {
	prizes := []Prize{
		{Label: "never", Amount: 100, Weight: 0},
		{Label: "always", Amount: 1, Weight: 3},
	}
	for roll := int64(0); roll < 3; roll++ {
		r := roll
		if got := Pick(prizes, func(int64) int64 { return r }); got.Label != "always" {
			t.Fatalf("roll %d picked %q", roll, got.Label)
		}
	}
}

func TestPickTotalWeightPassedToRand(t *testing.T) {
	prizes := []Prize{{Label: "a", Amount: 0, Weight: 4}, {Label: "b", Amount: 1, Weight: 6}}
	var seen int64
	Pick(prizes, func(max int64) int64 {
		seen = max
		return 0
	})
	if seen != 10 {
		t.Fatalf("rand upper bound = %d, want 10", seen)
	}
}

func TestPickEmptyPoolIsSafe(t *testing.T) {
	if got := Pick(nil, nil); got.Amount != 0 {
		t.Fatalf("empty pool should be a miss, got %+v", got)
	}
}

func TestResolveHardCapClamps(t *testing.T) {
	rt := Normalize(Runtime{HardCap: 2, Prizes: []Prize{{Label: "big", Amount: 10, Weight: 1}}})
	res := Resolve(rt, Prize{Label: "big", Amount: 10, Weight: 1}, 0)
	if res.Status != StatusWin || res.Amount != 2 {
		t.Fatalf("hard cap not applied: %+v", res)
	}
}

func TestResolveBudgetPartialPayout(t *testing.T) {
	rt := Normalize(Runtime{DailyBudget: 10, Prizes: DefaultPrizes()})
	res := Resolve(rt, Prize{Label: "big", Amount: 5, Weight: 1}, 7.5)
	if res.Status != StatusWin {
		t.Fatalf("expected partial win, got %+v", res)
	}
	if math.Abs(res.Amount-2.5) > 1e-9 {
		t.Fatalf("amount = %v, want 2.5", res.Amount)
	}
}

func TestResolveBudgetExhaustedDowngradesToMiss(t *testing.T) {
	rt := Normalize(Runtime{DailyBudget: 10, Prizes: DefaultPrizes()})
	res := Resolve(rt, Prize{Label: "big", Amount: 5, Weight: 1}, 10)
	if res.Status != StatusMiss || res.Amount != 0 {
		t.Fatalf("expected miss when budget spent, got %+v", res)
	}
}

func TestResolveZeroAmountIsMiss(t *testing.T) {
	rt := Normalize(Runtime{Prizes: DefaultPrizes()})
	res := Resolve(rt, Prize{Label: "谢谢参与", Amount: 0, Weight: 40}, 0)
	if res.Status != StatusMiss {
		t.Fatalf("zero prize should be a miss: %+v", res)
	}
}

func TestResolveUnlimitedBudget(t *testing.T) {
	rt := Normalize(Runtime{DailyBudget: 0, Prizes: DefaultPrizes()})
	res := Resolve(rt, Prize{Label: "x", Amount: 3, Weight: 1}, 99999)
	if res.Status != StatusWin || res.Amount != 3 {
		t.Fatalf("budget 0 must mean unlimited: %+v", res)
	}
}

func TestNormalizeFallsBackWhenAllWeightsZero(t *testing.T) {
	rt := Normalize(Runtime{Prizes: []Prize{{Label: "a", Amount: 1, Weight: 0}}})
	if TotalWeight(rt.Prizes) <= 0 {
		t.Fatal("normalize must guarantee a drawable pool")
	}
}

func TestNormalizeClampsNegativeAndOversized(t *testing.T) {
	rt := Normalize(Runtime{
		Prizes:      []Prize{{Label: "  ", Amount: -5, Weight: -3}, {Label: "big", Amount: MaxPrizeAmount * 10, Weight: MaxWeight * 2}},
		DailyBudget: -1,
		HardCap:     -1,
	})
	for _, p := range rt.Prizes {
		if p.Amount < 0 || p.Weight < 0 {
			t.Fatalf("negative survived: %+v", p)
		}
		if p.Amount > MaxPrizeAmount || p.Weight > MaxWeight {
			t.Fatalf("oversized survived: %+v", p)
		}
		if p.Label == "" {
			t.Fatal("empty label survived")
		}
	}
	if rt.DailyBudget < 0 || rt.HardCap < 0 {
		t.Fatalf("negative budget/cap survived: %+v", rt)
	}
}

func TestNormalizeTruncatesTooManyPrizes(t *testing.T) {
	in := make([]Prize, MaxPrizes+5)
	for i := range in {
		in[i] = Prize{Label: "p", Amount: 1, Weight: 1}
	}
	rt := Normalize(Runtime{Prizes: in})
	if len(rt.Prizes) != MaxPrizes {
		t.Fatalf("len = %d, want %d", len(rt.Prizes), MaxPrizes)
	}
}

func TestValidateRejectsZeroTotalWeight(t *testing.T) {
	err := Validate(Runtime{Prizes: []Prize{{Label: "a", Amount: 1, Weight: 0}}})
	if err == nil {
		t.Fatal("expected error for zero total weight")
	}
}

func TestPrizesRoundTripThroughJSON(t *testing.T) {
	want := DefaultPrizes()
	got, err := ParsePrizes(PrizesToJSON(want))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("len %d want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("prize %d: got %+v want %+v", i, got[i], want[i])
		}
	}
}

func TestMaxPrizeValueHonoursHardCap(t *testing.T) {
	rt := Runtime{HardCap: 2, Prizes: []Prize{{Label: "a", Amount: 10, Weight: 1}}}
	if got := MaxPrizeValue(rt); got != 2 {
		t.Fatalf("got %v want 2", got)
	}
}

func TestPrizeTypeMapping(t *testing.T) {
	if (Prize{Amount: 0}).Type() != "none" {
		t.Fatal("zero amount must map to none")
	}
	if (Prize{Amount: 1}).Type() != "balance" {
		t.Fatal("positive amount must map to balance")
	}
}
