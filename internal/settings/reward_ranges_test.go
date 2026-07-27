package settings

import (
	"context"
	"path/filepath"
	"testing"

	"sub2api-ext/internal/config"
	"sub2api-ext/internal/store"
)

func TestUpdatePersistsRewardRanges(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := New(st, config.CheckinConfig{Enabled: true, RewardAmount: 0.1, Timezone: "Asia/Shanghai"})

	mode := ModeRandom
	minV, maxV := 1.0, 10.0
	hardCap := 10.0
	ranges := []RewardRange{
		{Label: "小奖", Min: 1, Max: 2, Weight: 90},
		{Label: "大奖", Min: 8, Max: 10, Weight: 10},
	}
	rt, err := svc.Update(context.Background(), UpdateInput{
		RewardMode:   &mode,
		RewardMin:    &minV,
		RewardMax:    &maxV,
		HardCap:      &hardCap,
		RewardRanges: &ranges,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rt.RewardRanges) != 2 || rt.RewardRanges[1].Label != "大奖" {
		t.Fatalf("reward ranges not applied: %+v", rt.RewardRanges)
	}

	reloaded := New(st, config.CheckinConfig{Enabled: true, RewardAmount: 0.1, Timezone: "Asia/Shanghai"})
	got := reloaded.Get()
	if len(got.RewardRanges) != 2 || got.RewardRanges[0].Min != 1 || got.RewardRanges[1].Max != 10 {
		t.Fatalf("reward ranges not reloaded: %+v", got.RewardRanges)
	}
}

func TestPickRewardUsesConfiguredRange(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := New(st, config.CheckinConfig{Enabled: true, RewardAmount: 0.1, Timezone: "Asia/Shanghai"})

	mode := ModeRandom
	minV, maxV := 1.0, 10.0
	hardCap := 10.0
	ranges := []RewardRange{{Label: "only", Min: 7, Max: 8, Weight: 1}}
	if _, err := svc.Update(context.Background(), UpdateInput{
		RewardMode:   &mode,
		RewardMin:    &minV,
		RewardMax:    &maxV,
		HardCap:      &hardCap,
		RewardRanges: &ranges,
	}); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 20; i++ {
		reward := svc.PickReward()
		if reward < 7 || reward > 8 {
			t.Fatalf("reward %.4f outside configured range", reward)
		}
	}
}

func TestUpdateRejectsZeroWeightRewardRanges(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := New(st, config.CheckinConfig{Enabled: true, RewardAmount: 0.1, Timezone: "Asia/Shanghai"})

	mode := ModeRandom
	minV, maxV := 1.0, 2.0
	ranges := []RewardRange{{Label: "off", Min: 1, Max: 2, Weight: 0}}
	if _, err := svc.Update(context.Background(), UpdateInput{
		RewardMode:   &mode,
		RewardMin:    &minV,
		RewardMax:    &maxV,
		RewardRanges: &ranges,
	}); err == nil {
		t.Fatal("expected zero total weight to be rejected")
	}
}
