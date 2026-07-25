package settings_test

import (
	"context"
	"path/filepath"
	"testing"

	"sub2api-ext/internal/config"
	"sub2api-ext/internal/settings"
	"sub2api-ext/internal/store"
)

func TestAdminAPIKeyOverride(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	svc := settings.New(st, config.CheckinConfig{
		Enabled:      true,
		RewardAmount: 0.1,
		Timezone:     "Asia/Shanghai",
		NotesPrefix:  "daily-checkin",
	})
	ctx := context.Background()
	if svc.StoredAdminAPIKey() != "" {
		t.Fatalf("expected empty stored key")
	}
	if err := svc.SetAdminAPIKey(ctx, "  test-admin-key-ABCDEF  "); err != nil {
		t.Fatal(err)
	}
	if got := svc.StoredAdminAPIKey(); got != "test-admin-key-ABCDEF" {
		t.Fatalf("got %q", got)
	}
	// reload from store
	svc2 := settings.New(st, config.CheckinConfig{Enabled: true, RewardAmount: 0.1, Timezone: "Asia/Shanghai", NotesPrefix: "x"})
	if got := svc2.StoredAdminAPIKey(); got != "test-admin-key-ABCDEF" {
		t.Fatalf("reload got %q", got)
	}
	if m := settings.MaskSecret("test-admin-key-ABCDEF"); m != "****CDEF" {
		t.Fatalf("mask %q", m)
	}
	if err := svc2.ClearAdminAPIKey(ctx); err != nil {
		t.Fatal(err)
	}
	if svc2.StoredAdminAPIKey() != "" {
		t.Fatalf("expected cleared")
	}
}
