package modules_test

import (
	"testing"

	"sub2api-ext/internal/modules"
)

func TestLotteryModuleRegistered(t *testing.T) {
	ids := modules.ActiveIDs()
	found := false
	for _, id := range ids {
		if id == "lottery" {
			found = true
		}
	}
	if !found {
		t.Fatalf("lottery not in active modules: %v", ids)
	}
	// previously shipped modules must stay registered
	for _, want := range []string{"checkin", "account-patrol", "notify"} {
		ok := false
		for _, id := range ids {
			if id == want {
				ok = true
			}
		}
		if !ok {
			t.Fatalf("module %q disappeared: %v", want, ids)
		}
	}
}

func TestDailyReportModuleRegistered(t *testing.T) {
	ids := modules.ActiveIDs()
	found := false
	for _, id := range ids {
		if id == "daily-report" {
			found = true
		}
	}
	if !found {
		t.Fatalf("daily-report not in active modules: %v", ids)
	}
	for _, m := range modules.Builtin() {
		if m.ID != "daily-report" {
			continue
		}
		if m.AdminPath == "" || m.APIBase == "" {
			t.Fatalf("daily-report paths missing: %+v", m)
		}
		if m.Status != "active" {
			t.Fatalf("daily-report should be active, got %s", m.Status)
		}
		return
	}
	t.Fatal("daily-report module not in Builtin()")
}
