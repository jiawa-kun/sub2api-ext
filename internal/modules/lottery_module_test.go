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
	// the three shipped modules must stay registered
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
