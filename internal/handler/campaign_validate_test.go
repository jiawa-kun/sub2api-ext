package handler

import (
	"strings"
	"testing"

	"sub2api-ext/internal/store"
)

func TestValidateCampaignMeta(t *testing.T) {
	base := &store.RankCampaign{
		Name: "t", Board: store.CampaignBoardRewards,
		StartDate: "2026-07-01", EndDate: "2026-07-07",
		Status:      store.CampaignStatusActive,
		RewardsJSON: `[{"rank":1,"amount":1}]`,
	}
	if err := validateCampaignMeta(base, true); err != nil {
		t.Fatalf("ok meta: %v", err)
	}

	badDate := *base
	badDate.StartDate, badDate.EndDate = "2026-07-10", "2026-07-01"
	if err := validateCampaignMeta(&badDate, true); err == nil || !strings.Contains(err.Error(), "start_date") {
		t.Fatalf("want date error, got %v", err)
	}

	zeroReward := *base
	zeroReward.RewardsJSON = `[{"rank":1,"amount":0}]`
	if err := validateCampaignMeta(&zeroReward, true); err == nil || !strings.Contains(err.Error(), "positive") {
		t.Fatalf("want positive reward error, got %v", err)
	}

	cancelled := *base
	cancelled.Status = store.CampaignStatusCancelled
	if err := validateCampaignMeta(&cancelled, true); err == nil {
		t.Fatal("cancelled should not be settleable")
	}

	consumption := *base
	consumption.Board = store.CampaignBoardConsumption
	if err := validateCampaignMeta(&consumption, true); err != nil {
		t.Fatalf("consumption board should be allowed: %v", err)
	}
}

func TestPlanAndSettleReady(t *testing.T) {
	c := &store.RankCampaign{BudgetCap: 10, RewardsJSON: `[{"rank":1,"amount":2},{"rank":2,"amount":1}]`}
	rules, err := c.ParseRewards()
	if err != nil {
		t.Fatal(err)
	}
	rows := []campaignRankRow{{UserID: 1, Amount: 9}, {UserID: 2, Amount: 8}}
	payable, skipped, spend, details := planCampaignAwards(c, rows, rules, nil)
	if payable != 2 || skipped != 0 || spend != 3 || len(details) != 2 {
		t.Fatalf("payable=%d skipped=%d spend=%v details=%d", payable, skipped, spend, len(details))
	}
	if err := validateCampaignSettleReady(rows, payable); err != nil {
		t.Fatal(err)
	}

	// already success -> no payable
	existing := map[int64]store.RankCampaignAward{
		1: {UserID: 1, Status: "success", Amount: 2},
		2: {UserID: 2, Status: "success", Amount: 1},
	}
	payable, skipped, _, _ = planCampaignAwards(c, rows, rules, existing)
	if payable != 0 || skipped != 2 {
		t.Fatalf("expected all skipped, payable=%d skipped=%d", payable, skipped)
	}
	if err := validateCampaignSettleReady(rows, payable); err == nil {
		t.Fatal("expected no payable awards error")
	}

	// Existing success spend must still consume the period budget on retries.
	partialRows := []campaignRankRow{{UserID: 1, Amount: 9}, {UserID: 2, Amount: 8}}
	partialExisting := map[int64]store.RankCampaignAward{
		1: {UserID: 1, Status: "success", Amount: 2},
		2: {UserID: 2, Status: "failed", Amount: 1},
	}
	partialCampaign := &store.RankCampaign{BudgetCap: 2}
	payable, skipped, spend, details = planCampaignAwards(partialCampaign, partialRows, rules, partialExisting)
	if payable != 0 || skipped != 2 || spend != 2 || details[1]["status"] != "budget_cut" {
		t.Fatalf("existing spend must consume budget: payable=%d skipped=%d spend=%v details=%v", payable, skipped, spend, details)
	}
	if err := validateCampaignSettleReady(nil, 0); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected empty ranking error, got %v", err)
	}

	// budget cut leaves only first payable
	c2 := &store.RankCampaign{BudgetCap: 2, RewardsJSON: c.RewardsJSON}
	rules2, _ := c2.ParseRewards()
	payable, skipped, spend, _ = planCampaignAwards(c2, rows, rules2, nil)
	if payable != 1 || skipped != 1 || spend != 2 {
		t.Fatalf("budget cut payable=%d skipped=%d spend=%v", payable, skipped, spend)
	}
}

func TestHasPositiveRewardRule(t *testing.T) {
	if hasPositiveRewardRule(nil) {
		t.Fatal("nil")
	}
	if hasPositiveRewardRule([]store.RankRewardRule{{Rank: 1, Amount: 0}}) {
		t.Fatal("zero")
	}
	if !hasPositiveRewardRule([]store.RankRewardRule{{RankFrom: 1, RankTo: 3, Amount: 0.5}}) {
		t.Fatal("range positive")
	}
}

func TestValidateRewardRules(t *testing.T) {
	valid := []store.RankRewardRule{{Rank: 1, Amount: 2}, {RankFrom: 2, RankTo: 5, Amount: 1}}
	if err := validateRewardRules(valid); err != nil {
		t.Fatalf("valid rules: %v", err)
	}
	if err := validateRewardRules([]store.RankRewardRule{{Rank: 1, Amount: 2}, {RankFrom: 1, RankTo: 5, Amount: 1}}); err != nil {
		t.Fatalf("exact rank override should be valid: %v", err)
	}
	tests := []struct {
		name  string
		rules []store.RankRewardRule
		want  string
	}{
		{"zero amount", []store.RankRewardRule{{Rank: 1, Amount: 0}}, "amount"},
		{"negative rank", []store.RankRewardRule{{Rank: -1, Amount: 1}}, "invalid rank"},
		{"missing rank", []store.RankRewardRule{{Amount: 1}}, "range"},
		{"mixed rank", []store.RankRewardRule{{Rank: 1, RankFrom: 1, RankTo: 2, Amount: 1}}, "mix"},
		{"overlap", []store.RankRewardRule{{RankFrom: 1, RankTo: 3, Amount: 1}, {RankFrom: 3, RankTo: 5, Amount: 2}}, "overlapping"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateRewardRules(tt.rules); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want %q error, got %v", tt.want, err)
			}
		})
	}
}
