package patrol

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

const (
	KeyEnabled                 = "patrol_enabled"
	KeyCron                    = "patrol_cron"
	KeyGroups                  = "patrol_groups"
	KeyTestModel               = "patrol_test_model"
	KeyConcurrency             = "patrol_concurrency"
	KeyTimeoutMs               = "patrol_timeout_ms"
	KeyActionOnFail            = "patrol_action_on_fail"
	KeyOnlySchedulable         = "patrol_only_schedulable"
	KeyStopOnFirstModelFailure = "patrol_stop_on_first_model_failure"
	KeyPrompt                  = "patrol_prompt"
	KeyAutoEnableOnSuccess     = "patrol_auto_enable_on_success"
	KeyTimezone                = "patrol_timezone"
	KeyKeepRuns                = "patrol_keep_runs"
	KeyFailThreshold           = "patrol_fail_threshold"

	ActionDisable = "disable"
	ActionDelete  = "delete"
	ActionNone    = "none"
)

// Runtime is hot-reloadable patrol config.
type Runtime struct {
	Enabled                 bool     `json:"enabled"`
	Cron                    string   `json:"cron"`
	Groups                  []string `json:"groups"`
	TestModel               string   `json:"test_model"`
	Concurrency             int      `json:"concurrency"`
	TimeoutMs               int      `json:"timeout_ms"`
	ActionOnFail            string   `json:"action_on_fail"`
	OnlySchedulable         bool     `json:"only_schedulable"`
	StopOnFirstModelFailure bool     `json:"stop_on_first_model_failure"`
	Prompt                  string   `json:"prompt"`
	AutoEnableOnSuccess     bool     `json:"auto_enable_on_success"`
	Timezone                string   `json:"timezone"`
	KeepRuns                int      `json:"keep_runs"`
	FailThreshold           int      `json:"fail_threshold"`
}

// UpdateInput uses pointers so partial updates are possible.
type UpdateInput struct {
	Enabled                 *bool     `json:"enabled"`
	Cron                    *string   `json:"cron"`
	Groups                  *[]string `json:"groups"`
	TestModel               *string   `json:"test_model"`
	Concurrency             *int      `json:"concurrency"`
	TimeoutMs               *int      `json:"timeout_ms"`
	ActionOnFail            *string   `json:"action_on_fail"`
	OnlySchedulable         *bool     `json:"only_schedulable"`
	StopOnFirstModelFailure *bool     `json:"stop_on_first_model_failure"`
	Prompt                  *string   `json:"prompt"`
	AutoEnableOnSuccess     *bool     `json:"auto_enable_on_success"`
	Timezone                *string   `json:"timezone"`
	KeepRuns                *int      `json:"keep_runs"`
	FailThreshold           *int      `json:"fail_threshold"`
}

// Settings holds runtime patrol configuration backed by app_settings.
type Settings struct {
	mu      sync.RWMutex
	store   *store.Store
	current Runtime
}

func NewSettings(st *store.Store, defaults config.PatrolConfig) *Settings {
	s := &Settings{
		store:   st,
		current: normalizeRuntime(fromConfig(defaults)),
	}
	_ = s.Reload(context.Background())
	return s
}

func fromConfig(c config.PatrolConfig) Runtime {
	return Runtime{
		Enabled:                 c.Enabled,
		Cron:                    c.Cron,
		Groups:                  append([]string{}, c.Groups...),
		TestModel:               c.TestModel,
		Concurrency:            c.Concurrency,
		TimeoutMs:               c.TimeoutMs,
		ActionOnFail:            c.ActionOnFail,
		OnlySchedulable:         c.OnlySchedulable,
		StopOnFirstModelFailure: c.StopOnFirstModelFailure,
		Prompt:                  c.Prompt,
		AutoEnableOnSuccess:     c.AutoEnableOnSuccess,
		Timezone:                c.Timezone,
		KeepRuns:                c.KeepRuns,
		FailThreshold:           c.FailThreshold,
	}
}

func (s *Settings) Get() Runtime {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneRuntime(s.current)
}

func (s *Settings) Reload(ctx context.Context) error {
	rt := s.Get()
	// start from current in-memory defaults, overlay sqlite
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
	if v, ok := get(KeyCron); ok && strings.TrimSpace(v) != "" {
		rt.Cron = strings.TrimSpace(v)
	}
	if v, ok := get(KeyGroups); ok {
		rt.Groups = parseGroups(v)
	}
	if v, ok := get(KeyTestModel); ok {
		rt.TestModel = strings.TrimSpace(v)
	}
	if v, ok := get(KeyConcurrency); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			rt.Concurrency = n
		}
	}
	if v, ok := get(KeyTimeoutMs); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			rt.TimeoutMs = n
		}
	}
	if v, ok := get(KeyActionOnFail); ok && strings.TrimSpace(v) != "" {
		rt.ActionOnFail = strings.TrimSpace(v)
	}
	if v, ok := get(KeyOnlySchedulable); ok {
		rt.OnlySchedulable = parseBool(v, rt.OnlySchedulable)
	}
	if v, ok := get(KeyStopOnFirstModelFailure); ok {
		rt.StopOnFirstModelFailure = parseBool(v, rt.StopOnFirstModelFailure)
	}
	if v, ok := get(KeyPrompt); ok && v != "" {
		rt.Prompt = v
	}
	if v, ok := get(KeyAutoEnableOnSuccess); ok {
		rt.AutoEnableOnSuccess = parseBool(v, rt.AutoEnableOnSuccess)
	}
	if v, ok := get(KeyTimezone); ok && strings.TrimSpace(v) != "" {
		rt.Timezone = strings.TrimSpace(v)
	}
	if v, ok := get(KeyKeepRuns); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			rt.KeepRuns = n
		}
	}
	if v, ok := get(KeyFailThreshold); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			rt.FailThreshold = n
		}
	}
	rt = normalizeRuntime(rt)
	s.mu.Lock()
	s.current = rt
	s.mu.Unlock()
	return nil
}

func (s *Settings) Update(ctx context.Context, in UpdateInput) (Runtime, error) {
	cur := s.Get()
	next := cur
	if in.Enabled != nil {
		next.Enabled = *in.Enabled
	}
	if in.Cron != nil {
		next.Cron = strings.TrimSpace(*in.Cron)
	}
	if in.Groups != nil {
		next.Groups = normalizeGroups(*in.Groups)
	}
	if in.TestModel != nil {
		next.TestModel = strings.TrimSpace(*in.TestModel)
	}
	if in.Concurrency != nil {
		next.Concurrency = *in.Concurrency
	}
	if in.TimeoutMs != nil {
		next.TimeoutMs = *in.TimeoutMs
	}
	if in.ActionOnFail != nil {
		next.ActionOnFail = strings.TrimSpace(*in.ActionOnFail)
	}
	if in.OnlySchedulable != nil {
		next.OnlySchedulable = *in.OnlySchedulable
	}
	if in.StopOnFirstModelFailure != nil {
		next.StopOnFirstModelFailure = *in.StopOnFirstModelFailure
	}
	if in.Prompt != nil {
		next.Prompt = *in.Prompt
	}
	if in.AutoEnableOnSuccess != nil {
		next.AutoEnableOnSuccess = *in.AutoEnableOnSuccess
	}
	if in.Timezone != nil {
		next.Timezone = strings.TrimSpace(*in.Timezone)
	}
	if in.KeepRuns != nil {
		next.KeepRuns = *in.KeepRuns
	}
	if in.FailThreshold != nil {
		next.FailThreshold = *in.FailThreshold
	}
	next = normalizeRuntime(next)
	if err := validateRuntime(next); err != nil {
		return cur, err
	}

	kv := map[string]string{
		KeyEnabled:                 strconv.FormatBool(next.Enabled),
		KeyCron:                    next.Cron,
		KeyGroups:                  groupsToJSON(next.Groups),
		KeyTestModel:               next.TestModel,
		KeyConcurrency:             strconv.Itoa(next.Concurrency),
		KeyTimeoutMs:               strconv.Itoa(next.TimeoutMs),
		KeyActionOnFail:            next.ActionOnFail,
		KeyOnlySchedulable:         strconv.FormatBool(next.OnlySchedulable),
		KeyStopOnFirstModelFailure: strconv.FormatBool(next.StopOnFirstModelFailure),
		KeyPrompt:                  next.Prompt,
		KeyAutoEnableOnSuccess:     strconv.FormatBool(next.AutoEnableOnSuccess),
		KeyTimezone:                next.Timezone,
		KeyKeepRuns:                strconv.Itoa(next.KeepRuns),
		KeyFailThreshold:           strconv.Itoa(next.FailThreshold),
	}
	if err := s.store.SetSettings(ctx, kv); err != nil {
		return cur, err
	}
	s.mu.Lock()
	s.current = next
	s.mu.Unlock()
	return cloneRuntime(next), nil
}

func normalizeRuntime(rt Runtime) Runtime {
	rt.Cron = strings.TrimSpace(rt.Cron)
	if rt.Cron == "" {
		rt.Cron = "0 */6 * * *"
	}
	rt.Groups = normalizeGroups(rt.Groups)
	rt.TestModel = strings.TrimSpace(rt.TestModel)
	if rt.Concurrency <= 0 {
		rt.Concurrency = 8
	}
	if rt.Concurrency > 50 {
		rt.Concurrency = 50
	}
	if rt.TimeoutMs < 1000 {
		rt.TimeoutMs = 45000
	}
	switch strings.ToLower(strings.TrimSpace(rt.ActionOnFail)) {
	case ActionDisable, ActionDelete, ActionNone:
		rt.ActionOnFail = strings.ToLower(strings.TrimSpace(rt.ActionOnFail))
	default:
		rt.ActionOnFail = ActionDisable
	}
	if strings.TrimSpace(rt.Prompt) == "" {
		rt.Prompt = "hi"
	}
	if strings.TrimSpace(rt.Timezone) == "" {
		rt.Timezone = "Asia/Shanghai"
	}
	if rt.KeepRuns <= 0 {
		rt.KeepRuns = 50
	}
	if rt.FailThreshold <= 0 {
		rt.FailThreshold = 1
	}
	if rt.FailThreshold > 10 {
		rt.FailThreshold = 10
	}
	return rt
}

func validateRuntime(rt Runtime) error {
	if _, err := ParseCron(rt.Cron); err != nil {
		return fmt.Errorf("invalid cron: %w", err)
	}
	switch rt.ActionOnFail {
	case ActionDisable, ActionDelete, ActionNone:
	default:
		return fmt.Errorf("invalid action_on_fail: %s", rt.ActionOnFail)
	}
	if rt.Concurrency < 1 || rt.Concurrency > 50 {
		return fmt.Errorf("concurrency must be 1..50")
	}
	if rt.TimeoutMs < 1000 {
		return fmt.Errorf("timeout_ms must be >= 1000")
	}
	if rt.FailThreshold < 1 || rt.FailThreshold > 10 {
		return fmt.Errorf("fail_threshold must be 1..10")
	}
	return nil
}

func cloneRuntime(rt Runtime) Runtime {
	out := rt
	out.Groups = append([]string{}, rt.Groups...)
	return out
}

func normalizeGroups(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, g := range in {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		if _, ok := seen[g]; ok {
			continue
		}
		seen[g] = struct{}{}
		out = append(out, g)
	}
	return out
}

func parseGroups(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if strings.HasPrefix(raw, "[") {
		var arr []string
		if err := json.Unmarshal([]byte(raw), &arr); err == nil {
			return normalizeGroups(arr)
		}
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == ';'
	})
	return normalizeGroups(parts)
}

func groupsToJSON(groups []string) string {
	b, _ := json.Marshal(normalizeGroups(groups))
	return string(b)
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
