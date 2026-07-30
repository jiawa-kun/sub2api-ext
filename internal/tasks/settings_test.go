package tasks_test

import (
	"context"
	"path/filepath"
	"testing"

	"sub2api-ext/internal/store"
	"sub2api-ext/internal/tasks"
)

func TestDefaultRewardsAreZero(t *testing.T) {
	rt := tasks.DefaultRuntime()
	if !rt.Enabled {
		t.Fatal("enabled")
	}
	if len(rt.Defs) == 0 {
		t.Fatal("no defs")
	}
	for _, d := range rt.Defs {
		if d.Reward != 0 {
			t.Fatalf("default reward for %s = %v, want 0", d.ID, d.Reward)
		}
	}
}

func TestSettingsSaveReload(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := tasks.NewSettings(st)
	rt := s.Get()
	rt.Defs[0].Reward = 0.25
	rt.Defs[0].Enabled = true
	if err := s.Save(context.Background(), rt); err != nil {
		t.Fatal(err)
	}
	s2 := tasks.NewSettings(st)
	got := s2.Get()
	if got.Defs[0].Reward != 0.25 {
		t.Fatalf("reward=%v", got.Defs[0].Reward)
	}
}
