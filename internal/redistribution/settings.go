package redistribution

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"sub2api-ext/internal/patrol"
	"sub2api-ext/internal/store"
)

const SettingsKey = "redistribution_settings_json"

const (
	LogicAll = "all"
	LogicAny = "any"

	RuleNoActiveDays = "no_active_days"
	RuleNoUsageDays  = "no_usage_days"
	// Deprecated rule names are kept only so old JSON can be decoded safely.
	RuleNoExtensionDays     = "no_extension_days"
	RuleAccountAgeDays      = "account_age_days"
	RuleBalanceAtLeast      = "balance_at_least"
	RuleActiveWithinDays    = "active_within_days"
	RuleUsedWithinDays      = "used_within_days"
	RuleExtensionWithinDays = "extension_within_days"
	RuleTotalUsageAtLeast   = "total_usage_at_least"

	ReclaimFixed   = "fixed"
	ReclaimPercent = "percent"
	ReclaimExcess  = "excess"

	DistributionAuto  = "auto"
	DistributionClaim = "claim"

	DrawFixed       = "fixed"
	DrawRandom      = "random"
	DrawActiveShare = "active_share"

	ActiveShareEqual = "equal"
	ActiveShareUsage = "usage"

	AllocationEqual         = "equal"
	AllocationFixed         = "fixed"
	AllocationUsageWeighted = "usage_weighted"
	AllocationMixed         = "mixed"
)

type Rule struct {
	Type    string  `json:"type"`
	Enabled bool    `json:"enabled"`
	Days    int     `json:"days,omitempty"`
	Amount  float64 `json:"amount,omitempty"`
}

type ReclaimPolicy struct {
	Mode           string  `json:"mode"`
	Value          float64 `json:"value"`
	MinBalance     float64 `json:"min_balance"`
	ReserveBalance float64 `json:"reserve_balance"`
	MinPerUser     float64 `json:"min_per_user"`
	MaxPerUser     float64 `json:"max_per_user"`
	BatchCap       float64 `json:"batch_cap"`
}

type AllocationPolicy struct {
	Mode             string  `json:"mode"`
	FixedAmount      float64 `json:"fixed_amount"`
	EqualRatio       float64 `json:"equal_ratio"`
	MinReward        float64 `json:"min_reward"`
	MaxRewardPerUser float64 `json:"max_reward_per_user"`
	RecipientLimit   int     `json:"recipient_limit"`
}

type Runtime struct {
	Enabled                 bool             `json:"enabled"`
	AutoExecute             bool             `json:"auto_execute"`
	Cron                    string           `json:"cron"`
	Timezone                string           `json:"timezone"`
	RequirePreview          bool             `json:"require_preview"`
	InactiveLogic           string           `json:"inactive_logic"`
	InactiveRules           []Rule           `json:"inactive_rules"`
	ActiveLogic             string           `json:"active_logic"`
	ActiveRules             []Rule           `json:"active_rules"`
	NewUserProtectionDays   int              `json:"new_user_protection_days"`
	ExcludeUserIDs          []int64          `json:"exclude_user_ids"`
	Reclaim                 ReclaimPolicy    `json:"reclaim"`
	DistributionMode        string           `json:"distribution_mode"`
	ClaimExpireDays         int              `json:"claim_expire_days"`
	PoolExpireDays          int              `json:"pool_expire_days"`
	DrawMode                string           `json:"draw_mode"`
	DrawFixedAmount         float64          `json:"draw_fixed_amount"`
	DrawMinAmount           float64          `json:"draw_min_amount"`
	DrawMaxAmount           float64          `json:"draw_max_amount"`
	ActiveShareMode         string           `json:"active_share_mode"`
	Allocation              AllocationPolicy `json:"allocation"`
	MaxUsers                int              `json:"max_users"`
	FailureThresholdPercent float64          `json:"failure_threshold_percent"`
	KeepBatches             int              `json:"keep_batches"`
}

func DefaultRuntime() Runtime {
	return Runtime{
		Enabled:        false,
		AutoExecute:    false,
		Cron:           "0 3 1 * *",
		Timezone:       "Asia/Shanghai",
		RequirePreview: true,
		InactiveLogic:  LogicAll,
		InactiveRules: []Rule{
			{Type: RuleNoActiveDays, Enabled: true, Days: 60},
			{Type: RuleNoUsageDays, Enabled: true, Days: 60},
		},
		ActiveLogic: LogicAny,
		ActiveRules: []Rule{
			{Type: RuleActiveWithinDays, Enabled: true, Days: 30},
			{Type: RuleUsedWithinDays, Enabled: true, Days: 30},
		},
		NewUserProtectionDays: 30,
		Reclaim: ReclaimPolicy{
			Mode: ReclaimFixed, Value: 0.5, MinBalance: 1, ReserveBalance: 0.5,
			MinPerUser: 0.01, MaxPerUser: 1,
		},
		DistributionMode: DistributionAuto,
		ClaimExpireDays:  7,
		PoolExpireDays:   7,
		DrawMode:         DrawActiveShare,
		DrawFixedAmount:  0.1,
		DrawMinAmount:    0.01,
		DrawMaxAmount:    0.5,
		ActiveShareMode:  ActiveShareEqual,
		Allocation: AllocationPolicy{
			Mode: AllocationMixed, EqualRatio: 50, MinReward: 0.01,
			MaxRewardPerUser: 0.5, RecipientLimit: 100,
		},
		MaxUsers:                5000,
		FailureThresholdPercent: 10,
		KeepBatches:             50,
	}
}

type Settings struct {
	mu      sync.RWMutex
	store   *store.Store
	current Runtime
}

func NewSettings(st *store.Store) *Settings {
	s := &Settings{store: st, current: DefaultRuntime()}
	_ = s.Reload(context.Background())
	return s
}

func (s *Settings) Get() Runtime {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneRuntime(s.current)
}

func (s *Settings) Reload(ctx context.Context) error {
	rt := DefaultRuntime()
	raw, ok, err := s.store.GetSetting(ctx, SettingsKey)
	if err != nil {
		return err
	}
	if ok && strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &rt); err != nil {
			return err
		}
	}
	rt = normalizeRuntime(rt)
	if err := validateRuntime(rt); err != nil {
		return err
	}
	s.mu.Lock()
	s.current = rt
	s.mu.Unlock()
	return nil
}

func (s *Settings) Save(ctx context.Context, rt Runtime) (Runtime, error) {
	rt = normalizeRuntime(rt)
	if err := validateRuntime(rt); err != nil {
		return s.Get(), err
	}
	raw, err := json.Marshal(rt)
	if err != nil {
		return s.Get(), err
	}
	if err := s.store.SetSetting(ctx, SettingsKey, string(raw)); err != nil {
		return s.Get(), err
	}
	s.mu.Lock()
	s.current = rt
	s.mu.Unlock()
	return cloneRuntime(rt), nil
}

func normalizeRuntime(rt Runtime) Runtime {
	def := DefaultRuntime()
	rt.Cron = strings.TrimSpace(rt.Cron)
	if rt.Cron == "" {
		rt.Cron = def.Cron
	}
	rt.Timezone = strings.TrimSpace(rt.Timezone)
	if rt.Timezone == "" {
		rt.Timezone = def.Timezone
	}
	rt.InactiveLogic = normalizeLogic(rt.InactiveLogic, LogicAll)
	rt.ActiveLogic = normalizeLogic(rt.ActiveLogic, LogicAny)
	rt.InactiveRules = normalizeRules(rt.InactiveRules, true)
	if len(rt.InactiveRules) == 0 {
		rt.InactiveRules = cloneRules(def.InactiveRules)
	}
	rt.ActiveRules = normalizeRules(rt.ActiveRules, false)
	if len(rt.ActiveRules) == 0 {
		rt.ActiveRules = cloneRules(def.ActiveRules)
	}
	if rt.NewUserProtectionDays < 0 {
		rt.NewUserProtectionDays = 0
	}
	rt.ExcludeUserIDs = normalizeIDs(rt.ExcludeUserIDs)

	switch strings.ToLower(strings.TrimSpace(rt.Reclaim.Mode)) {
	case ReclaimFixed, ReclaimPercent, ReclaimExcess:
		rt.Reclaim.Mode = strings.ToLower(strings.TrimSpace(rt.Reclaim.Mode))
	default:
		rt.Reclaim.Mode = def.Reclaim.Mode
	}
	if rt.Reclaim.Value < 0 {
		rt.Reclaim.Value = 0
	}
	if rt.Reclaim.MinBalance < 0 {
		rt.Reclaim.MinBalance = 0
	}
	if rt.Reclaim.ReserveBalance < 0 {
		rt.Reclaim.ReserveBalance = 0
	}
	if rt.Reclaim.MinPerUser < 0 {
		rt.Reclaim.MinPerUser = 0
	}
	if rt.Reclaim.MaxPerUser < 0 {
		rt.Reclaim.MaxPerUser = 0
	}
	if rt.Reclaim.BatchCap < 0 {
		rt.Reclaim.BatchCap = 0
	}

	switch strings.ToLower(strings.TrimSpace(rt.DistributionMode)) {
	case DistributionAuto, DistributionClaim:
		rt.DistributionMode = strings.ToLower(strings.TrimSpace(rt.DistributionMode))
	default:
		rt.DistributionMode = def.DistributionMode
	}
	if rt.ClaimExpireDays <= 0 {
		rt.ClaimExpireDays = def.ClaimExpireDays
	}
	if rt.PoolExpireDays <= 0 {
		rt.PoolExpireDays = def.PoolExpireDays
	}
	if rt.DrawFixedAmount < 0 {
		rt.DrawFixedAmount = 0
	}
	if rt.DrawMinAmount < 0 {
		rt.DrawMinAmount = 0
	}
	if rt.DrawMaxAmount < 0 {
		rt.DrawMaxAmount = 0
	}
	switch strings.ToLower(strings.TrimSpace(rt.DrawMode)) {
	case DrawFixed, DrawRandom, DrawActiveShare:
		rt.DrawMode = strings.ToLower(strings.TrimSpace(rt.DrawMode))
	default:
		rt.DrawMode = def.DrawMode
	}
	switch strings.ToLower(strings.TrimSpace(rt.ActiveShareMode)) {
	case ActiveShareEqual, ActiveShareUsage:
		rt.ActiveShareMode = strings.ToLower(strings.TrimSpace(rt.ActiveShareMode))
	default:
		rt.ActiveShareMode = def.ActiveShareMode
	}
	switch strings.ToLower(strings.TrimSpace(rt.Allocation.Mode)) {
	case AllocationEqual, AllocationFixed, AllocationUsageWeighted, AllocationMixed:
		rt.Allocation.Mode = strings.ToLower(strings.TrimSpace(rt.Allocation.Mode))
	default:
		rt.Allocation.Mode = def.Allocation.Mode
	}
	if rt.Allocation.EqualRatio < 0 {
		rt.Allocation.EqualRatio = 0
	}
	if rt.Allocation.EqualRatio > 100 {
		rt.Allocation.EqualRatio = 100
	}
	if rt.Allocation.FixedAmount < 0 {
		rt.Allocation.FixedAmount = 0
	}
	if rt.Allocation.MinReward < 0 {
		rt.Allocation.MinReward = 0
	}
	if rt.Allocation.MaxRewardPerUser < 0 {
		rt.Allocation.MaxRewardPerUser = 0
	}
	if rt.Allocation.RecipientLimit <= 0 {
		rt.Allocation.RecipientLimit = def.Allocation.RecipientLimit
	}
	if rt.Allocation.RecipientLimit > 5000 {
		rt.Allocation.RecipientLimit = 5000
	}
	if rt.MaxUsers <= 0 {
		rt.MaxUsers = def.MaxUsers
	}
	if rt.MaxUsers > 20000 {
		rt.MaxUsers = 20000
	}
	if rt.FailureThresholdPercent < 0 {
		rt.FailureThresholdPercent = 0
	}
	if rt.FailureThresholdPercent > 100 {
		rt.FailureThresholdPercent = 100
	}
	if rt.KeepBatches <= 0 {
		rt.KeepBatches = def.KeepBatches
	}
	if rt.KeepBatches > 500 {
		rt.KeepBatches = 500
	}
	return rt
}

func validateRuntime(rt Runtime) error {
	if _, err := patrol.ParseCron(rt.Cron); err != nil {
		return fmt.Errorf("invalid cron: %w", err)
	}
	if _, err := time.LoadLocation(rt.Timezone); err != nil {
		return fmt.Errorf("invalid timezone: %w", err)
	}
	if countEnabled(rt.InactiveRules) == 0 {
		return fmt.Errorf("至少启用一个不活跃条件")
	}
	if countEnabled(rt.ActiveRules) == 0 {
		return fmt.Errorf("至少启用一个活跃条件")
	}
	if rt.Reclaim.Mode == ReclaimPercent && (rt.Reclaim.Value <= 0 || rt.Reclaim.Value > 100) {
		return fmt.Errorf("比例回收值必须在 0 到 100 之间")
	}
	if rt.Reclaim.Mode != ReclaimPercent && rt.Reclaim.Value <= 0 && rt.Reclaim.Mode != ReclaimExcess {
		return fmt.Errorf("回收额度必须大于 0")
	}
	if rt.Reclaim.MaxPerUser > 0 && rt.Reclaim.MinPerUser > rt.Reclaim.MaxPerUser {
		return fmt.Errorf("单用户最低回收额不能高于最高回收额")
	}
	if rt.Allocation.Mode == AllocationFixed && rt.Allocation.FixedAmount <= 0 {
		return fmt.Errorf("固定分配额度必须大于 0")
	}
	if rt.DrawMode == DrawFixed && rt.DrawFixedAmount <= 0 {
		return fmt.Errorf("固定抽取额度必须大于 0")
	}
	if rt.DrawMode == DrawRandom {
		if rt.DrawMinAmount <= 0 || rt.DrawMaxAmount < rt.DrawMinAmount {
			return fmt.Errorf("随机抽取额度范围无效")
		}
	}
	if rt.DrawMinAmount < 0 || rt.DrawMaxAmount < 0 || math.IsNaN(rt.DrawMinAmount) || math.IsInf(rt.DrawMinAmount, 0) || math.IsNaN(rt.DrawMaxAmount) || math.IsInf(rt.DrawMaxAmount, 0) {
		return fmt.Errorf("invalid draw amount")
	}
	if err := validateRules(rt.InactiveRules, true); err != nil {
		return err
	}
	if err := validateRules(rt.ActiveRules, false); err != nil {
		return err
	}
	if math.IsNaN(rt.Reclaim.Value) || math.IsInf(rt.Reclaim.Value, 0) {
		return fmt.Errorf("invalid reclaim value")
	}
	return nil
}

func normalizeRules(in []Rule, inactive bool) []Rule {
	known := map[string]bool{}
	if inactive {
		for _, v := range []string{RuleNoActiveDays, RuleNoUsageDays} {
			known[v] = true
		}
	} else {
		for _, v := range []string{RuleActiveWithinDays, RuleUsedWithinDays} {
			known[v] = true
		}
	}
	out := make([]Rule, 0, len(in))
	for _, rule := range in {
		rule.Type = strings.ToLower(strings.TrimSpace(rule.Type))
		if !known[rule.Type] {
			continue
		}
		if rule.Days < 0 {
			rule.Days = 0
		}
		if rule.Amount < 0 {
			rule.Amount = 0
		}
		out = append(out, rule)
	}
	return out
}

func validateRules(rules []Rule, inactive bool) error {
	allowed := map[string]bool{}
	if inactive {
		allowed[RuleNoActiveDays] = true
		allowed[RuleNoUsageDays] = true
	} else {
		allowed[RuleActiveWithinDays] = true
		allowed[RuleUsedWithinDays] = true
	}
	for _, rule := range rules {
		if !allowed[rule.Type] {
			return fmt.Errorf("不支持的活跃条件: %s", rule.Type)
		}
		if !rule.Enabled {
			continue
		}
		if rule.Days <= 0 {
			return fmt.Errorf("条件 %s 的天数必须大于 0", rule.Type)
		}
	}
	return nil
}

func normalizeLogic(v, fallback string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == LogicAny {
		return LogicAny
	}
	if v == LogicAll {
		return LogicAll
	}
	return fallback
}

func normalizeIDs(in []int64) []int64 {
	seen := map[int64]bool{}
	out := make([]int64, 0, len(in))
	for _, id := range in {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func countEnabled(rules []Rule) int {
	n := 0
	for _, rule := range rules {
		if rule.Enabled {
			n++
		}
	}
	return n
}

func cloneRules(in []Rule) []Rule { return append([]Rule(nil), in...) }

func cloneRuntime(rt Runtime) Runtime {
	out := rt
	out.InactiveRules = cloneRules(rt.InactiveRules)
	out.ActiveRules = cloneRules(rt.ActiveRules)
	out.ExcludeUserIDs = append([]int64(nil), rt.ExcludeUserIDs...)
	return out
}
