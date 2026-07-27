package settings

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"sub2api-ext/internal/config"
	"sub2api-ext/internal/store"
)

const (
	KeyEnabled          = "checkin_enabled"
	KeyRewardMode       = "checkin_reward_mode"
	KeyRewardAmount     = "checkin_reward_amount"
	KeyRewardMin        = "checkin_reward_min"
	KeyRewardMax        = "checkin_reward_max"
	KeyRewardRanges     = "checkin_reward_ranges"
	KeyTimezone         = "checkin_timezone"
	KeyNotesPrefix      = "checkin_notes_prefix"
	KeyHardCap          = "checkin_hard_cap"
	KeyDailyBudget      = "checkin_daily_budget"
	KeyBudgetAction     = "checkin_budget_action"
	KeyStreakEnabled    = "checkin_streak_enabled"
	KeyStreakStep       = "checkin_streak_step"
	KeyStreakMaxDays    = "checkin_streak_max_days"
	KeyStreakMilestones = "checkin_streak_milestones"
	// Sub2API admin API key override (UI-managed; takes precedence over env)
	KeySub2APIAdminAPIKey = "sub2api_admin_api_key"
)

const (
	ModeFixed  = "fixed"
	ModeRandom = "random"

	BudgetBlock   = "block"
	BudgetDisable = "disable"

	AbsoluteMax          = 100.0
	DefaultHardCap       = 5.0
	DefaultDailyBudget   = 50.0
	DefaultStreakStep    = 0.01
	DefaultStreakMaxDays = 7
	MaxRewardRanges      = 12
	MaxRewardRangeWeight = 1000000
)

type RewardRange struct {
	Label  string  `json:"label,omitempty"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
	Weight int     `json:"weight"`
}

type Runtime struct {
	Enabled          bool            `json:"enabled"`
	RewardMode       string          `json:"reward_mode"`
	RewardAmount     float64         `json:"reward_amount"`
	RewardMin        float64         `json:"reward_min"`
	RewardMax        float64         `json:"reward_max"`
	RewardRanges     []RewardRange   `json:"reward_ranges,omitempty"`
	RandomReward     bool            `json:"random_reward"`
	Timezone         string          `json:"timezone"`
	NotesPrefix      string          `json:"notes_prefix"`
	HardCap          float64         `json:"hard_cap"`
	DailyBudget      float64         `json:"daily_budget"`
	BudgetAction     string          `json:"budget_action"`
	Clamped          bool            `json:"clamped,omitempty"`
	StreakEnabled    bool            `json:"streak_enabled"`
	StreakStep       float64         `json:"streak_step"`
	StreakMaxDays    int             `json:"streak_max_days"`
	StreakMilestones map[int]float64 `json:"streak_milestones"`
}

type Service struct {
	mu          sync.RWMutex
	store       *store.Store
	current     Runtime
	adminAPIKey string // SQLite override; empty means fall back to env
}

func New(st *store.Store, defaults config.CheckinConfig) *Service {
	amt := defaults.RewardAmount
	if amt <= 0 {
		amt = 0.1
	}
	s := &Service{
		store: st,
		current: Runtime{
			Enabled:          defaults.Enabled,
			RewardMode:       ModeFixed,
			RewardAmount:     amt,
			RewardMin:        amt,
			RewardMax:        amt,
			RandomReward:     false,
			Timezone:         defaults.Timezone,
			NotesPrefix:      defaults.NotesPrefix,
			HardCap:          DefaultHardCap,
			DailyBudget:      DefaultDailyBudget,
			BudgetAction:     BudgetBlock,
			StreakEnabled:    true,
			StreakStep:       DefaultStreakStep,
			StreakMaxDays:    DefaultStreakMaxDays,
			StreakMilestones: defaultMilestones(),
		},
	}
	_ = s.Reload(context.Background())
	return s
}

func (s *Service) Get() Runtime {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current
}

func (s *Service) Location() *time.Location {
	rt := s.Get()
	loc, err := time.LoadLocation(rt.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

func (s *Service) Today() string {
	return time.Now().In(s.Location()).Format("2006-01-02")
}

func (s *Service) PickReward() float64 {
	rt := s.Get()
	var reward float64
	if rt.RewardMode == ModeRandom {
		minV, maxV := normalizeRange(rt.RewardMin, rt.RewardMax, rt.RewardAmount)
		if rr, ok := pickRewardRange(rt.RewardRanges); ok {
			minV, maxV = normalizeRange(rr.Min, rr.Max, rr.Min)
		}
		if maxV > minV {
			reward = roundMoney(randFloatBetween(minV, maxV))
		} else {
			reward = roundMoney(minV)
		}
	} else {
		amt := rt.RewardAmount
		if amt <= 0 {
			amt = rt.RewardMin
		}
		if amt <= 0 {
			amt = 0.01
		}
		reward = roundMoney(amt)
	}
	if rt.HardCap > 0 && reward > rt.HardCap {
		reward = roundMoney(rt.HardCap)
	}
	return reward
}

func (s *Service) Reload(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.current
	clamped := false

	if v, ok, err := s.store.GetSetting(ctx, KeyEnabled); err != nil {
		return err
	} else if ok {
		cur.Enabled = parseBool(v, cur.Enabled)
	}

	legacyAmt := cur.RewardAmount
	if v, ok, err := s.store.GetSetting(ctx, KeyRewardAmount); err != nil {
		return err
	} else if ok {
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && f > 0 {
			legacyAmt = f
		}
	}
	minV, maxV := legacyAmt, legacyAmt
	if v, ok, err := s.store.GetSetting(ctx, KeyRewardMin); err != nil {
		return err
	} else if ok {
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && f > 0 {
			minV = f
		}
	}
	if v, ok, err := s.store.GetSetting(ctx, KeyRewardMax); err != nil {
		return err
	} else if ok {
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && f > 0 {
			maxV = f
		}
	}
	rewardRanges := cur.RewardRanges
	if v, ok, err := s.store.GetSetting(ctx, KeyRewardRanges); err != nil {
		return err
	} else if ok {
		if ranges, err := parseRewardRanges(v); err == nil {
			rewardRanges = ranges
		}
	}
	if maxV < minV {
		maxV = minV
	}
	if legacyAmt <= 0 {
		legacyAmt = minV
	}

	mode := ModeFixed
	if v, ok, err := s.store.GetSetting(ctx, KeyRewardMode); err != nil {
		return err
	} else if ok {
		mode = normalizeMode(v)
	} else if maxV > minV+1e-12 {
		mode = ModeRandom
	}

	hardCap := DefaultHardCap
	if v, ok, err := s.store.GetSetting(ctx, KeyHardCap); err != nil {
		return err
	} else if ok {
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && f > 0 {
			hardCap = f
		}
	}
	if hardCap > AbsoluteMax {
		hardCap = AbsoluteMax
		clamped = true
	}

	dailyBudget := DefaultDailyBudget
	if v, ok, err := s.store.GetSetting(ctx, KeyDailyBudget); err != nil {
		return err
	} else if ok {
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && f >= 0 {
			dailyBudget = f
		}
	}

	budgetAction := BudgetBlock
	if v, ok, err := s.store.GetSetting(ctx, KeyBudgetAction); err != nil {
		return err
	} else if ok {
		budgetAction = normalizeBudgetAction(v)
	}

	if legacyAmt > hardCap {
		legacyAmt = hardCap
		clamped = true
	}
	if minV > hardCap {
		minV = hardCap
		clamped = true
	}
	if maxV > hardCap {
		maxV = hardCap
		clamped = true
	}
	if maxV < minV {
		maxV = minV
	}
	rewardRanges, rangesClamped := normalizeRewardRanges(rewardRanges, hardCap)
	if rangesClamped {
		clamped = true
	}

	cur.RewardMode = mode
	cur.RewardAmount = legacyAmt
	cur.RewardMin = minV
	cur.RewardMax = maxV
	cur.RewardRanges = rewardRanges
	cur.RandomReward = mode == ModeRandom
	cur.HardCap = hardCap
	cur.DailyBudget = dailyBudget
	cur.BudgetAction = budgetAction
	cur.Clamped = clamped

	if v, ok, err := s.store.GetSetting(ctx, KeyTimezone); err != nil {
		return err
	} else if ok && strings.TrimSpace(v) != "" {
		if _, err := time.LoadLocation(strings.TrimSpace(v)); err == nil {
			cur.Timezone = strings.TrimSpace(v)
		}
	}
	if v, ok, err := s.store.GetSetting(ctx, KeyNotesPrefix); err != nil {
		return err
	} else if ok && strings.TrimSpace(v) != "" {
		cur.NotesPrefix = strings.TrimSpace(v)
	}

	// streak
	if v, ok, err := s.store.GetSetting(ctx, KeyStreakEnabled); err != nil {
		return err
	} else if ok {
		cur.StreakEnabled = parseBool(v, cur.StreakEnabled)
	} else if cur.StreakMaxDays == 0 {
		cur.StreakEnabled = true
	}
	if v, ok, err := s.store.GetSetting(ctx, KeyStreakStep); err != nil {
		return err
	} else if ok {
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && f >= 0 {
			cur.StreakStep = f
		}
	} else if cur.StreakStep <= 0 {
		cur.StreakStep = DefaultStreakStep
	}
	if v, ok, err := s.store.GetSetting(ctx, KeyStreakMaxDays); err != nil {
		return err
	} else if ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			cur.StreakMaxDays = n
		}
	} else if cur.StreakMaxDays <= 0 {
		cur.StreakMaxDays = DefaultStreakMaxDays
	}
	if cur.StreakMaxDays > 365 {
		cur.StreakMaxDays = 365
	}
	if v, ok, err := s.store.GetSetting(ctx, KeyStreakMilestones); err != nil {
		return err
	} else if ok && strings.TrimSpace(v) != "" {
		if m, err := parseMilestones(v); err == nil {
			cur.StreakMilestones = m
		}
	} else if len(cur.StreakMilestones) == 0 {
		cur.StreakMilestones = defaultMilestones()
	}

	// load admin api key override (not part of Runtime reward settings)
	if v, ok, err := s.store.GetSetting(ctx, KeySub2APIAdminAPIKey); err != nil {
		return err
	} else if ok {
		s.adminAPIKey = strings.TrimSpace(v)
	} else {
		s.adminAPIKey = ""
	}

	s.current = cur
	return nil
}

type UpdateInput struct {
	Enabled          *bool           `json:"enabled"`
	RewardMode       *string         `json:"reward_mode"`
	RewardAmount     *float64        `json:"reward_amount"`
	RewardMin        *float64        `json:"reward_min"`
	RewardMax        *float64        `json:"reward_max"`
	RewardRanges     *[]RewardRange  `json:"reward_ranges"`
	Timezone         *string         `json:"timezone"`
	NotesPrefix      *string         `json:"notes_prefix"`
	HardCap          *float64        `json:"hard_cap"`
	DailyBudget      *float64        `json:"daily_budget"`
	BudgetAction     *string         `json:"budget_action"`
	StreakEnabled    *bool           `json:"streak_enabled"`
	StreakStep       *float64        `json:"streak_step"`
	StreakMaxDays    *int            `json:"streak_max_days"`
	StreakMilestones map[int]float64 `json:"streak_milestones"`
}

func (s *Service) Update(ctx context.Context, in UpdateInput) (Runtime, error) {
	kv := map[string]string{}
	next := s.Get()
	clamped := false

	if in.Enabled != nil {
		next.Enabled = *in.Enabled
		kv[KeyEnabled] = strconv.FormatBool(next.Enabled)
	}

	mode := next.RewardMode
	if in.RewardMode != nil {
		mode = normalizeMode(*in.RewardMode)
	}

	hardCap := next.HardCap
	if hardCap <= 0 {
		hardCap = DefaultHardCap
	}
	if in.HardCap != nil {
		hardCap = *in.HardCap
	}
	if hardCap <= 0 {
		return next, fmt.Errorf("hard_cap must be > 0")
	}
	if hardCap > AbsoluteMax {
		return next, fmt.Errorf("hard_cap must be <= %.0f", AbsoluteMax)
	}

	fixedAmt := next.RewardAmount
	if in.RewardAmount != nil {
		fixedAmt = *in.RewardAmount
	}
	minV, maxV := next.RewardMin, next.RewardMax
	if in.RewardMin != nil {
		minV = *in.RewardMin
	}
	if in.RewardMax != nil {
		maxV = *in.RewardMax
	}
	rewardRanges := next.RewardRanges
	if in.RewardRanges != nil {
		rewardRanges = append([]RewardRange{}, (*in.RewardRanges)...)
	}

	if in.RewardMode == nil {
		if in.RewardMin != nil || in.RewardMax != nil {
			if maxV > minV+1e-12 {
				mode = ModeRandom
			} else if in.RewardAmount == nil {
				mode = ModeFixed
				if fixedAmt <= 0 {
					fixedAmt = minV
				}
			}
		} else if in.RewardAmount != nil {
			mode = ModeFixed
		}
	}

	if fixedAmt > hardCap {
		fixedAmt = hardCap
		clamped = true
	}
	if minV > hardCap {
		minV = hardCap
		clamped = true
	}
	if maxV > hardCap {
		maxV = hardCap
		clamped = true
	}
	if err := validateRewardRanges(rewardRanges); err != nil {
		return next, err
	}
	rewardRanges, rangesClamped := normalizeRewardRanges(rewardRanges, hardCap)
	if rangesClamped {
		clamped = true
	}

	if mode == ModeRandom {
		if minV <= 0 || maxV <= 0 {
			return next, fmt.Errorf("random mode: reward_min/max must be > 0")
		}
		if maxV < minV {
			return next, fmt.Errorf("reward_max must be >= reward_min")
		}
		if maxV > AbsoluteMax {
			return next, fmt.Errorf("reward_max too large")
		}
		next.RewardMode = ModeRandom
		next.RewardMin = minV
		next.RewardMax = maxV
		next.RewardRanges = rewardRanges
		next.RandomReward = true
		next.RewardAmount = roundMoney((minV + maxV) / 2)
		kv[KeyRewardMode] = ModeRandom
		kv[KeyRewardMin] = formatFloat(minV)
		kv[KeyRewardMax] = formatFloat(maxV)
		kv[KeyRewardAmount] = formatFloat(next.RewardAmount)
		if in.RewardRanges != nil || rangesClamped {
			kv[KeyRewardRanges] = formatRewardRanges(rewardRanges)
		}
	} else {
		if fixedAmt <= 0 {
			if minV > 0 && (in.RewardMin != nil || in.RewardMax != nil) {
				fixedAmt = minV
			} else {
				return next, fmt.Errorf("fixed mode: reward_amount must be > 0")
			}
		}
		if fixedAmt > AbsoluteMax {
			return next, fmt.Errorf("reward_amount too large")
		}
		next.RewardMode = ModeFixed
		next.RewardAmount = fixedAmt
		next.RewardMin = fixedAmt
		next.RewardMax = fixedAmt
		next.RewardRanges = nil
		next.RandomReward = false
		kv[KeyRewardMode] = ModeFixed
		kv[KeyRewardAmount] = formatFloat(fixedAmt)
		kv[KeyRewardMin] = formatFloat(fixedAmt)
		kv[KeyRewardMax] = formatFloat(fixedAmt)
		if in.RewardRanges != nil {
			kv[KeyRewardRanges] = "[]"
		}
	}

	next.HardCap = hardCap
	kv[KeyHardCap] = formatFloat(hardCap)

	if in.DailyBudget != nil {
		if *in.DailyBudget < 0 {
			return next, fmt.Errorf("daily_budget must be >= 0")
		}
		if *in.DailyBudget > AbsoluteMax*1000 {
			return next, fmt.Errorf("daily_budget too large")
		}
		next.DailyBudget = *in.DailyBudget
		kv[KeyDailyBudget] = formatFloat(next.DailyBudget)
	}

	if in.BudgetAction != nil {
		next.BudgetAction = normalizeBudgetAction(*in.BudgetAction)
		kv[KeyBudgetAction] = next.BudgetAction
	}

	if in.Timezone != nil {
		tz := strings.TrimSpace(*in.Timezone)
		if tz == "" {
			return next, fmt.Errorf("timezone is required")
		}
		if _, err := time.LoadLocation(tz); err != nil {
			return next, fmt.Errorf("invalid timezone: %w", err)
		}
		next.Timezone = tz
		kv[KeyTimezone] = tz
	}
	if in.NotesPrefix != nil {
		p := strings.TrimSpace(*in.NotesPrefix)
		if p == "" {
			p = "daily-checkin"
		}
		next.NotesPrefix = p
		kv[KeyNotesPrefix] = p
	}

	if next.HardCap <= 0 {
		next.HardCap = DefaultHardCap
	}

	if next.BudgetAction == "" {
		next.BudgetAction = BudgetBlock
	}
	if in.StreakEnabled != nil {
		next.StreakEnabled = *in.StreakEnabled
		kv[KeyStreakEnabled] = strconv.FormatBool(next.StreakEnabled)
	}
	if in.StreakStep != nil {
		if *in.StreakStep < 0 {
			return next, fmt.Errorf("streak_step must be >= 0")
		}
		if *in.StreakStep > AbsoluteMax {
			return next, fmt.Errorf("streak_step too large")
		}
		next.StreakStep = *in.StreakStep
		kv[KeyStreakStep] = formatFloat(next.StreakStep)
	}
	if in.StreakMaxDays != nil {
		if *in.StreakMaxDays < 1 {
			return next, fmt.Errorf("streak_max_days must be >= 1")
		}
		if *in.StreakMaxDays > 365 {
			return next, fmt.Errorf("streak_max_days too large")
		}
		next.StreakMaxDays = *in.StreakMaxDays
		kv[KeyStreakMaxDays] = strconv.Itoa(next.StreakMaxDays)
	}
	if next.StreakMaxDays <= 0 {
		next.StreakMaxDays = DefaultStreakMaxDays
	}
	if in.StreakMilestones != nil {
		clean := map[int]float64{}
		for day, amt := range in.StreakMilestones {
			if day < 1 || day > 365 {
				return next, fmt.Errorf("milestone day must be 1..365")
			}
			if amt < 0 || amt > AbsoluteMax {
				return next, fmt.Errorf("milestone amount invalid for day %d", day)
			}
			if amt > 0 {
				clean[day] = roundMoney(amt)
			}
		}
		next.StreakMilestones = clean
		kv[KeyStreakMilestones] = formatMilestones(clean)
	}
	if next.StreakMilestones == nil {
		next.StreakMilestones = defaultMilestones()
	}
	next.Clamped = clamped

	if len(kv) == 0 {
		return next, fmt.Errorf("no fields to update")
	}
	if err := s.store.SetSettings(ctx, kv); err != nil {
		return next, err
	}
	s.mu.Lock()
	s.current = next
	s.mu.Unlock()
	return next, nil
}

// StoredAdminAPIKey returns the UI/SQLite override only (not env).
func (s *Service) StoredAdminAPIKey() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return strings.TrimSpace(s.adminAPIKey)
}

// SetAdminAPIKey persists a non-empty admin API key override and hot-loads it.
func (s *Service) SetAdminAPIKey(ctx context.Context, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("admin_api_key is empty")
	}
	if len(key) > 512 {
		return fmt.Errorf("admin_api_key too long")
	}
	if err := s.store.SetSetting(ctx, KeySub2APIAdminAPIKey, key); err != nil {
		return err
	}
	s.mu.Lock()
	s.adminAPIKey = key
	s.mu.Unlock()
	return nil
}

// ClearAdminAPIKey removes the SQLite override so env falls back.
func (s *Service) ClearAdminAPIKey(ctx context.Context) error {
	if err := s.store.DeleteSetting(ctx, KeySub2APIAdminAPIKey); err != nil {
		return err
	}
	s.mu.Lock()
	s.adminAPIKey = ""
	s.mu.Unlock()
	return nil
}

// MaskSecret returns a non-reversible display form (last 4 chars).
func MaskSecret(secret string) string {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return ""
	}
	if len(secret) <= 4 {
		return "****"
	}
	return "****" + secret[len(secret)-4:]
}

func ApplyMapToUpdateInput(m map[string]any) UpdateInput {
	in := UpdateInput{}
	if v, ok := m["enabled"].(bool); ok {
		in.Enabled = &v
	}
	if v, ok := asString(m["reward_mode"]); ok {
		in.RewardMode = &v
	}
	if v, ok := asFloat(m["reward_amount"]); ok {
		in.RewardAmount = &v
	}
	if v, ok := asFloat(m["reward_min"]); ok {
		in.RewardMin = &v
	}
	if v, ok := asFloat(m["reward_max"]); ok {
		in.RewardMax = &v
	}
	if raw, ok := m["reward_ranges"]; ok && raw != nil {
		if rr, ok := parseRewardRangesAny(raw); ok {
			in.RewardRanges = &rr
		}
	}
	if v, ok := asString(m["timezone"]); ok {
		in.Timezone = &v
	}
	if v, ok := asString(m["notes_prefix"]); ok {
		in.NotesPrefix = &v
	}
	if v, ok := asFloat(m["hard_cap"]); ok {
		in.HardCap = &v
	}
	if v, ok := asFloat(m["daily_budget"]); ok {
		in.DailyBudget = &v
	}
	if v, ok := asString(m["budget_action"]); ok {
		in.BudgetAction = &v
	}
	if v, ok := m["streak_enabled"].(bool); ok {
		in.StreakEnabled = &v
	}
	if v, ok := asFloat(m["streak_step"]); ok {
		in.StreakStep = &v
	}
	if v, ok := asFloat(m["streak_max_days"]); ok {
		n := int(v)
		in.StreakMaxDays = &n
	}
	if raw, ok := m["streak_milestones"]; ok && raw != nil {
		if mm, ok := parseMilestonesAny(raw); ok {
			in.StreakMilestones = mm
		}
	}
	return in
}

func asString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	default:
		return "", false
	}
}

func asFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	default:
		return 0, false
	}
}

func normalizeMode(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case ModeRandom, "rand", "range":
		return ModeRandom
	default:
		return ModeFixed
	}
}

func normalizeBudgetAction(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case BudgetDisable, "auto_disable", "off":
		return BudgetDisable
	default:
		return BudgetBlock
	}
}

func normalizeRange(minV, maxV, fallback float64) (float64, float64) {
	if minV <= 0 {
		minV = fallback
	}
	if maxV <= 0 {
		maxV = fallback
	}
	if minV <= 0 {
		minV = 0.01
	}
	if maxV < minV {
		maxV = minV
	}
	return minV, maxV
}

func normalizeRewardRanges(ranges []RewardRange, hardCap float64) ([]RewardRange, bool) {
	if len(ranges) == 0 {
		return nil, false
	}
	if len(ranges) > MaxRewardRanges {
		ranges = ranges[:MaxRewardRanges]
	}
	out := make([]RewardRange, 0, len(ranges))
	clamped := false
	for _, rr := range ranges {
		rr.Label = strings.TrimSpace(rr.Label)
		if len([]rune(rr.Label)) > 24 {
			rr.Label = string([]rune(rr.Label)[:24])
		}
		if rr.Min < 0 {
			rr.Min = 0
			clamped = true
		}
		if rr.Max < 0 {
			rr.Max = 0
			clamped = true
		}
		if rr.Max < rr.Min {
			rr.Max = rr.Min
			clamped = true
		}
		if hardCap > 0 {
			if rr.Min > hardCap {
				rr.Min = hardCap
				clamped = true
			}
			if rr.Max > hardCap {
				rr.Max = hardCap
				clamped = true
			}
		}
		if rr.Weight < 0 {
			rr.Weight = 0
			clamped = true
		}
		if rr.Weight > MaxRewardRangeWeight {
			rr.Weight = MaxRewardRangeWeight
			clamped = true
		}
		rr.Min = roundMoney(rr.Min)
		rr.Max = roundMoney(rr.Max)
		out = append(out, rr)
	}
	return out, clamped
}

func validateRewardRanges(ranges []RewardRange) error {
	if len(ranges) > MaxRewardRanges {
		return fmt.Errorf("random reward ranges cannot exceed %d", MaxRewardRanges)
	}
	total := 0
	for i, rr := range ranges {
		if rr.Min <= 0 || rr.Max <= 0 {
			return fmt.Errorf("random range %d min/max must be > 0", i+1)
		}
		if rr.Max < rr.Min {
			return fmt.Errorf("random range %d max must be >= min", i+1)
		}
		if rr.Max > AbsoluteMax {
			return fmt.Errorf("random range %d max too large", i+1)
		}
		if rr.Weight < 0 {
			return fmt.Errorf("random range %d weight cannot be negative", i+1)
		}
		if rr.Weight > MaxRewardRangeWeight {
			return fmt.Errorf("random range %d weight too large", i+1)
		}
		if rr.Weight > 0 {
			total += rr.Weight
		}
	}
	if len(ranges) > 0 && total <= 0 {
		return fmt.Errorf("random reward ranges total weight must be > 0")
	}
	return nil
}

func pickRewardRange(ranges []RewardRange) (RewardRange, bool) {
	total := 0
	for _, rr := range ranges {
		if rr.Weight > 0 {
			total += rr.Weight
		}
	}
	if total <= 0 {
		return RewardRange{}, false
	}
	n, ok := randInt(total)
	if !ok {
		for _, rr := range ranges {
			if rr.Weight > 0 {
				return rr, true
			}
		}
		return RewardRange{}, false
	}
	acc := 0
	for _, rr := range ranges {
		if rr.Weight <= 0 {
			continue
		}
		acc += rr.Weight
		if n < acc {
			return rr, true
		}
	}
	return RewardRange{}, false
}

func parseRewardRanges(raw string) ([]RewardRange, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var ranges []RewardRange
	if err := json.Unmarshal([]byte(raw), &ranges); err != nil {
		return nil, err
	}
	if err := validateRewardRanges(ranges); err != nil {
		return nil, err
	}
	out, _ := normalizeRewardRanges(ranges, 0)
	return out, nil
}

func parseRewardRangesAny(raw any) ([]RewardRange, bool) {
	switch v := raw.(type) {
	case []RewardRange:
		if err := validateRewardRanges(v); err != nil {
			return nil, false
		}
		out, _ := normalizeRewardRanges(v, 0)
		return out, true
	case []any:
		out := make([]RewardRange, 0, len(v))
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				return nil, false
			}
			rr := RewardRange{}
			if label, ok := asString(m["label"]); ok {
				rr.Label = label
			}
			if minV, ok := asFloat(m["min"]); ok {
				rr.Min = minV
			}
			if maxV, ok := asFloat(m["max"]); ok {
				rr.Max = maxV
			}
			if weight, ok := asFloat(m["weight"]); ok {
				rr.Weight = int(weight)
			}
			out = append(out, rr)
		}
		if err := validateRewardRanges(out); err != nil {
			return nil, false
		}
		clean, _ := normalizeRewardRanges(out, 0)
		return clean, true
	case string:
		out, err := parseRewardRanges(v)
		return out, err == nil
	default:
		return nil, false
	}
}

func formatRewardRanges(ranges []RewardRange) string {
	if len(ranges) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(ranges)
	return string(b)
}

func formatFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
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

func roundMoney(v float64) float64 {
	return math.Round(v*10000) / 10000
}

func randFloatBetween(minV, maxV float64) float64 {
	if maxV <= minV {
		return minV
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return minV
	}
	u := binary.BigEndian.Uint64(b[:])
	f := float64(u) / float64(^uint64(0))
	return minV + f*(maxV-minV)
}

func randInt(max int) (int, bool) {
	if max <= 0 {
		return 0, false
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, false
	}
	return int(binary.BigEndian.Uint64(b[:]) % uint64(max)), true
}

// PersistClamped writes currently clamped reward fields + hard_cap into sqlite so restart stays safe.
func (s *Service) PersistClamped(ctx context.Context) error {
	rt := s.Get()
	if !rt.Clamped {
		return nil
	}
	kv := map[string]string{
		KeyHardCap:      formatFloat(rt.HardCap),
		KeyRewardAmount: formatFloat(rt.RewardAmount),
		KeyRewardMin:    formatFloat(rt.RewardMin),
		KeyRewardMax:    formatFloat(rt.RewardMax),
		KeyRewardRanges: formatRewardRanges(rt.RewardRanges),
		KeyRewardMode:   rt.RewardMode,
		KeyDailyBudget:  formatFloat(rt.DailyBudget),
		KeyBudgetAction: rt.BudgetAction,
	}
	if err := s.store.SetSettings(ctx, kv); err != nil {
		return err
	}
	s.mu.Lock()
	rt.Clamped = false
	s.current = rt
	s.mu.Unlock()
	return nil
}

// ApplyTemplate returns UpdateInput for named templates.
func ApplyTemplate(name string) (UpdateInput, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "daily", "normal", "日常":
		en := true
		mode := ModeFixed
		amt := 0.1
		hc := 1.0
		bud := 50.0
		ba := BudgetBlock
		se := true
		step := 0.01
		md := 7
		miles := defaultMilestones()
		return UpdateInput{
			Enabled: &en, RewardMode: &mode, RewardAmount: &amt,
			HardCap: &hc, DailyBudget: &bud, BudgetAction: &ba,
			StreakEnabled: &se, StreakStep: &step, StreakMaxDays: &md,
			StreakMilestones: miles,
		}, nil
	case "promo", "活动", "event":
		en := true
		mode := ModeRandom
		minV, maxV := 0.1, 1.0
		hc := 2.0
		bud := 100.0
		ba := BudgetBlock
		se := true
		step := 0.02
		md := 7
		return UpdateInput{
			Enabled: &en, RewardMode: &mode, RewardMin: &minV, RewardMax: &maxV,
			HardCap: &hc, DailyBudget: &bud, BudgetAction: &ba,
			StreakEnabled: &se, StreakStep: &step, StreakMaxDays: &md,
		}, nil
	case "off", "close", "关闭":
		en := false
		return UpdateInput{Enabled: &en}, nil
	default:
		return UpdateInput{}, fmt.Errorf("unknown template: %s (daily|promo|off)", name)
	}
}

// StreakExtra computes step-based extra for streak length (excluding milestone).
func StreakExtra(rt Runtime, streakIncludingToday int) float64 {
	if !rt.StreakEnabled || streakIncludingToday <= 1 || rt.StreakStep <= 0 {
		return 0
	}
	maxDays := rt.StreakMaxDays
	if maxDays <= 1 {
		return 0
	}
	n := streakIncludingToday - 1
	if n > maxDays-1 {
		n = maxDays - 1
	}
	if n <= 0 {
		return 0
	}
	return roundMoney(float64(n) * rt.StreakStep)
}

// MilestoneBonus returns one-shot bonus when streak hits a configured day.
func MilestoneBonus(rt Runtime, streakIncludingToday int) float64 {
	if !rt.StreakEnabled || streakIncludingToday <= 0 || len(rt.StreakMilestones) == 0 {
		return 0
	}
	if amt, ok := rt.StreakMilestones[streakIncludingToday]; ok && amt > 0 {
		return roundMoney(amt)
	}
	return 0
}

// TotalStreakBonus = step extra + milestone.
func TotalStreakBonus(rt Runtime, streakIncludingToday int) (step float64, milestone float64, total float64) {
	step = StreakExtra(rt, streakIncludingToday)
	milestone = MilestoneBonus(rt, streakIncludingToday)
	total = roundMoney(step + milestone)
	return
}

func defaultMilestones() map[int]float64 {
	return map[int]float64{3: 0.05, 7: 0.2}
}

func formatMilestones(m map[int]float64) string {
	// encode as JSON object with string keys for stability
	type pair struct {
		d int
		a float64
	}
	// simple manual JSON
	parts := make([]string, 0, len(m))
	// stable-ish order by scanning days 1..365
	for d := 1; d <= 365; d++ {
		if a, ok := m[d]; ok {
			parts = append(parts, fmt.Sprintf(`"%d":%s`, d, formatFloat(a)))
		}
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func parseMilestones(raw string) (map[int]float64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultMilestones(), nil
	}
	// use encoding/json via map[string]any by minimal parser: std json
	var obj map[string]float64
	if err := jsonUnmarshalMilestones(raw, &obj); err != nil {
		return nil, err
	}
	out := map[int]float64{}
	for k, v := range obj {
		d, err := strconv.Atoi(k)
		if err != nil || d < 1 {
			continue
		}
		if v > 0 {
			out[d] = roundMoney(v)
		}
	}
	return out, nil
}

func parseMilestonesAny(raw any) (map[int]float64, bool) {
	switch v := raw.(type) {
	case map[string]any:
		out := map[int]float64{}
		for k, val := range v {
			d, err := strconv.Atoi(k)
			if err != nil {
				continue
			}
			f, ok := asFloat(val)
			if ok && f > 0 {
				out[d] = roundMoney(f)
			}
		}
		return out, true
	case map[string]float64:
		out := map[int]float64{}
		for k, f := range v {
			d, err := strconv.Atoi(k)
			if err != nil {
				continue
			}
			if f > 0 {
				out[d] = roundMoney(f)
			}
		}
		return out, true
	case string:
		m, err := parseMilestones(v)
		return m, err == nil
	default:
		return nil, false
	}
}

// isolated to avoid import cycle noise; json in settings package
func jsonUnmarshalMilestones(raw string, dest *map[string]float64) error {
	return json.Unmarshal([]byte(raw), dest)
}
