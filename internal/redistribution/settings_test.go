package redistribution

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"sub2api-ext/internal/store"
)

func TestValidateRuntimeRejectsEnabledZeroRuleValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Runtime)
		want   string
	}{
		{
			name: "inactive days",
			mutate: func(rt *Runtime) {
				rt.InactiveRules = []Rule{{Type: RuleNoActiveDays, Enabled: true, Days: 0}}
			},
			want: "天数必须大于 0",
		},
		{
			name: "active days",
			mutate: func(rt *Runtime) {
				rt.ActiveRules = []Rule{{Type: RuleActiveWithinDays, Enabled: true, Days: 0}}
			},
			want: "天数必须大于 0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := DefaultRuntime()
			tt.mutate(&rt)
			rt = normalizeRuntime(rt)
			if err := validateRuntime(rt); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want %q error, got %v", tt.want, err)
			}
		})
	}
}

func TestValidateRuntimeAllowsDisabledZeroRule(t *testing.T) {
	rt := DefaultRuntime()
	rt.InactiveRules = append(rt.InactiveRules, Rule{Type: RuleNoUsageDays, Enabled: false, Days: 0})
	rt = normalizeRuntime(rt)
	if err := validateRuntime(rt); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeRuntimeDropsLegacyActivityRules(t *testing.T) {
	rt := DefaultRuntime()
	rt.InactiveRules = []Rule{{Type: RuleBalanceAtLeast, Enabled: true, Amount: 1}}
	rt.ActiveRules = []Rule{{Type: RuleTotalUsageAtLeast, Enabled: true, Amount: 1}}
	got := normalizeRuntime(rt)
	if len(got.InactiveRules) != len(DefaultRuntime().InactiveRules) || len(got.ActiveRules) != len(DefaultRuntime().ActiveRules) {
		t.Fatalf("legacy rules should fall back to defaults: inactive=%+v active=%+v", got.InactiveRules, got.ActiveRules)
	}
}

func TestReloadRejectsStoredZeroRuleAndKeepsSafeDefault(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetSetting(context.Background(), SettingsKey, `{
		"enabled":true,
		"cron":"0 3 1 * *",
		"timezone":"Asia/Shanghai",
		"inactive_logic":"all",
		"inactive_rules":[{"type":"no_active_days","enabled":true,"days":0}],
		"active_logic":"any",
		"active_rules":[{"type":"active_within_days","enabled":true,"days":30}],
		"reclaim":{"mode":"fixed","value":1},
		"allocation":{"mode":"equal","recipient_limit":10}
	}`); err != nil {
		t.Fatal(err)
	}
	s := NewSettings(st)
	got := s.Get()
	if len(got.InactiveRules) == 0 || got.InactiveRules[0].Days <= 0 {
		t.Fatalf("unsafe rules loaded: %+v", got.InactiveRules)
	}
}
