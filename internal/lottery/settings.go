// Package lottery implements the daily draw module: a weighted prize pool
// whose labels, amounts and weights are fully operator-configurable, gated by
// its own daily budget so it can never spend beyond what the operator allows.
package lottery

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"sub2api-ext/internal/config"
	"sub2api-ext/internal/store"
)

// app_settings keys.
const (
	KeyEnabled        = "lottery_enabled"
	KeyPrizes         = "lottery_prizes"
	KeyDailyBudget    = "lottery_daily_budget"
	KeyHardCap        = "lottery_hard_cap"
	KeyRequireCheckin = "lottery_require_checkin"
)

// Limits protecting the operator from a mis-typed config.
const (
	MaxPrizes      = 12
	MaxWeight      = 1000000
	MaxPrizeAmount = 10000.0
	minAmountUnit  = 0.0001
)

// Prize is one configurable slot in the wheel.
type Prize struct {
	Label  string  `json:"label"`
	Amount float64 `json:"amount"`
	Weight int     `json:"weight"`
}

// IsWin reports whether the prize actually credits balance.
func (p Prize) IsWin() bool { return p.Amount >= minAmountUnit }

// Type maps a prize to the stored prize_type.
func (p Prize) Type() string {
	if p.IsWin() {
		return store.PrizeTypeBalance
	}
	return store.PrizeTypeNone
}

// Runtime is the hot-reloadable lottery configuration.
type Runtime struct {
	Enabled bool `json:"enabled"`
	// RequireCheckin gates the draw behind today's check-in.
	RequireCheckin bool    `json:"require_checkin"`
	Prizes         []Prize `json:"prizes"`
	// DailyBudget caps total credited amount per day; 0 means unlimited.
	DailyBudget float64 `json:"daily_budget"`
	// HardCap clamps any single prize; 0 means unclamped.
	HardCap float64 `json:"hard_cap"`
}

// UpdateInput allows partial updates.
type UpdateInput struct {
	Enabled        *bool    `json:"enabled"`
	RequireCheckin *bool    `json:"require_checkin"`
	Prizes         *[]Prize `json:"prizes"`
	DailyBudget    *float64 `json:"daily_budget"`
	HardCap        *float64 `json:"hard_cap"`
}

// Settings holds runtime lottery config backed by app_settings.
type Settings struct {
	mu      sync.RWMutex
	store   *store.Store
	current Runtime
}

func NewSettings(st *store.Store, defaults config.LotteryConfig) *Settings {
	s := &Settings{
		store:   st,
		current: Normalize(fromConfig(defaults)),
	}
	_ = s.Reload(context.Background())
	return s
}

func fromConfig(c config.LotteryConfig) Runtime {
	rt := Runtime{
		Enabled:        c.Enabled,
		RequireCheckin: c.RequireCheckin,
		DailyBudget:    c.DailyBudget,
		HardCap:        c.HardCap,
	}
	for _, p := range c.Prizes {
		rt.Prizes = append(rt.Prizes, Prize{Label: p.Label, Amount: p.Amount, Weight: p.Weight})
	}
	return rt
}

func (s *Settings) Get() Runtime {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Clone(s.current)
}

func (s *Settings) Reload(ctx context.Context) error {
	rt := s.Get()
	get := func(key string) (string, bool) {
		v, ok, err := s.store.GetSetting(ctx, key)
		if err != nil || !ok {
			return "", false
		}
		return v, true
	}
	if v, ok := get(KeyEnabled); ok {
		rt.Enabled = parseBool(v, rt.Enabled)
	}
	if v, ok := get(KeyRequireCheckin); ok {
		rt.RequireCheckin = parseBool(v, rt.RequireCheckin)
	}
	if v, ok := get(KeyPrizes); ok {
		if parsed, err := ParsePrizes(v); err == nil && len(parsed) > 0 {
			rt.Prizes = parsed
		}
	}
	if v, ok := get(KeyDailyBudget); ok {
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			rt.DailyBudget = f
		}
	}
	if v, ok := get(KeyHardCap); ok {
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			rt.HardCap = f
		}
	}
	rt = Normalize(rt)
	s.mu.Lock()
	s.current = rt
	s.mu.Unlock()
	return nil
}

// Update applies a partial change, validates it, then persists.
func (s *Settings) Update(ctx context.Context, in UpdateInput) (Runtime, error) {
	cur := s.Get()
	next := cur
	if in.Enabled != nil {
		next.Enabled = *in.Enabled
	}
	if in.RequireCheckin != nil {
		next.RequireCheckin = *in.RequireCheckin
	}
	if in.Prizes != nil {
		next.Prizes = append([]Prize{}, (*in.Prizes)...)
	}
	if in.DailyBudget != nil {
		next.DailyBudget = *in.DailyBudget
	}
	if in.HardCap != nil {
		next.HardCap = *in.HardCap
	}
	next = Normalize(next)
	if err := Validate(next); err != nil {
		return cur, err
	}
	kv := map[string]string{
		KeyEnabled:        strconv.FormatBool(next.Enabled),
		KeyRequireCheckin: strconv.FormatBool(next.RequireCheckin),
		KeyPrizes:         PrizesToJSON(next.Prizes),
		KeyDailyBudget:    strconv.FormatFloat(next.DailyBudget, 'f', -1, 64),
		KeyHardCap:        strconv.FormatFloat(next.HardCap, 'f', -1, 64),
	}
	if err := s.store.SetSettings(ctx, kv); err != nil {
		return cur, err
	}
	s.mu.Lock()
	s.current = next
	s.mu.Unlock()
	return Clone(next), nil
}

// DefaultPrizes is the starting pool; every field stays operator-editable.
func DefaultPrizes() []Prize {
	return []Prize{
		{Label: "宇宙边角料补贴", Amount: 2, Weight: 50},
		{Label: "赛博馒头基金", Amount: 5, Weight: 20},
		{Label: "老板良心残片", Amount: 10, Weight: 15},
		{Label: "财神打喷嚏奖", Amount: 20, Weight: 12},
		{Label: "天选打工皇帝奖", Amount: 50, Weight: 3},
	}
}

// Normalize clamps every field into a safe range. It never returns a pool
// with zero total weight, because that would make drawing impossible.
func Normalize(rt Runtime) Runtime {
	if len(rt.Prizes) == 0 {
		rt.Prizes = DefaultPrizes()
	}
	if len(rt.Prizes) > MaxPrizes {
		rt.Prizes = rt.Prizes[:MaxPrizes]
	}
	out := make([]Prize, 0, len(rt.Prizes))
	for _, p := range rt.Prizes {
		p.Label = strings.TrimSpace(p.Label)
		if p.Label == "" {
			p.Label = "神秘奖励"
		}
		if len([]rune(p.Label)) > 24 {
			p.Label = string([]rune(p.Label)[:24])
		}
		if p.Amount < 0 {
			p.Amount = 0
		}
		if p.Amount > MaxPrizeAmount {
			p.Amount = MaxPrizeAmount
		}
		p.Amount = roundAmount(p.Amount)
		if p.Weight < 0 {
			p.Weight = 0
		}
		if p.Weight > MaxWeight {
			p.Weight = MaxWeight
		}
		out = append(out, p)
	}
	if TotalWeight(out) <= 0 {
		out = DefaultPrizes()
	}
	rt.Prizes = out
	if rt.DailyBudget < 0 {
		rt.DailyBudget = 0
	}
	if rt.HardCap < 0 {
		rt.HardCap = 0
	}
	rt.DailyBudget = roundAmount(rt.DailyBudget)
	rt.HardCap = roundAmount(rt.HardCap)
	return rt
}

// Validate rejects configurations that would misbehave at draw time.
func Validate(rt Runtime) error {
	if len(rt.Prizes) == 0 {
		return fmt.Errorf("奖池不能为空")
	}
	if len(rt.Prizes) > MaxPrizes {
		return fmt.Errorf("奖项数量不能超过 %d", MaxPrizes)
	}
	if TotalWeight(rt.Prizes) <= 0 {
		return fmt.Errorf("奖池总权重必须大于 0")
	}
	for i, p := range rt.Prizes {
		if p.Weight < 0 {
			return fmt.Errorf("第 %d 个奖项权重不能为负", i+1)
		}
		if p.Amount < 0 {
			return fmt.Errorf("第 %d 个奖项额度不能为负", i+1)
		}
		if p.Amount > MaxPrizeAmount {
			return fmt.Errorf("第 %d 个奖项额度不能超过 %.0f", i+1, MaxPrizeAmount)
		}
	}
	if rt.HardCap < 0 {
		return fmt.Errorf("单次上限不能为负")
	}
	if rt.DailyBudget < 0 {
		return fmt.Errorf("日预算不能为负")
	}
	return nil
}

// TotalWeight sums prize weights.
func TotalWeight(prizes []Prize) int {
	total := 0
	for _, p := range prizes {
		if p.Weight > 0 {
			total += p.Weight
		}
	}
	return total
}

// MaxPrizeValue returns the largest configured amount, used to decide whether
// the remaining daily budget can still cover any winning prize.
func MaxPrizeValue(rt Runtime) float64 {
	max := 0.0
	for _, p := range rt.Prizes {
		if p.Weight <= 0 {
			continue
		}
		amt := p.Amount
		if rt.HardCap > 0 && amt > rt.HardCap {
			amt = rt.HardCap
		}
		if amt > max {
			max = amt
		}
	}
	return max
}

// Clone deep-copies a Runtime so callers cannot mutate shared state.
func Clone(rt Runtime) Runtime {
	out := rt
	out.Prizes = append([]Prize{}, rt.Prizes...)
	return out
}

// ParsePrizes reads the JSON array stored in app_settings.
func ParsePrizes(raw string) ([]Prize, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty prizes")
	}
	var arr []Prize
	if err := json.Unmarshal([]byte(raw), &arr); err != nil {
		return nil, err
	}
	return arr, nil
}

// PrizesToJSON serializes the pool for storage.
func PrizesToJSON(prizes []Prize) string {
	b, _ := json.Marshal(prizes)
	return string(b)
}

func roundAmount(v float64) float64 {
	return float64(int(v*10000+0.5)) / 10000
}

func parseBool(v string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}
