// Package report builds and delivers the daily operations digest.
//
// It answers "what happened yesterday" in one scheduled message so the
// operator never has to open the admin page to know whether check-in,
// lottery and patrol behaved. It owns no new business rule: every number is
// read back from the same SQLite tables the modules already write, and
// delivery reuses the notification channel configured in the notify module.
package report

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"sub2api-ext/internal/config"
	"sub2api-ext/internal/store"
)

// app_settings keys.
const (
	KeyEnabled  = "report_enabled"
	KeySendAt   = "report_send_at"
	KeyTimezone = "report_timezone"
	KeyCoverDay = "report_cover_day"
	KeySections = "report_sections"
	// KeyLastSent stores the last covered date that was actually delivered,
	// so a restart inside the send window cannot produce a duplicate report.
	KeyLastSent = "report_last_sent"
)

// Cover day modes.
const (
	CoverYesterday = "yesterday"
	CoverToday     = "today"
)

// Report sections.
const (
	SectionCheckin = "checkin"
	SectionLottery = "lottery"
	SectionPatrol  = "patrol"
)

// AllSections lists every selectable section.
func AllSections() []string {
	return []string{SectionCheckin, SectionLottery, SectionPatrol}
}

// SectionLabel returns a Chinese label for a section id.
func SectionLabel(s string) string {
	switch s {
	case SectionCheckin:
		return "签到"
	case SectionLottery:
		return "抽奖"
	case SectionPatrol:
		return "巡检"
	default:
		return s
	}
}

// CoverLabel returns a Chinese label for a cover mode.
func CoverLabel(c string) string {
	if c == CoverToday {
		return "当天（截至发送时刻）"
	}
	return "前一天（完整一天）"
}

// Runtime is the hot-reloadable report configuration.
type Runtime struct {
	Enabled bool `json:"enabled"`
	// SendAt is a local HH:MM wall-clock time.
	SendAt string `json:"send_at"`
	// Timezone the SendAt and the covered date are evaluated in.
	Timezone string `json:"timezone"`
	// CoverDay picks which day the digest describes.
	CoverDay string `json:"cover_day"`
	// Sections selects which blocks appear in the message.
	Sections []string `json:"sections"`
}

// UpdateInput allows partial updates.
type UpdateInput struct {
	Enabled  *bool     `json:"enabled"`
	SendAt   *string   `json:"send_at"`
	Timezone *string   `json:"timezone"`
	CoverDay *string   `json:"cover_day"`
	Sections *[]string `json:"sections"`
}

// Settings holds runtime report config backed by app_settings.
type Settings struct {
	mu      sync.RWMutex
	store   *store.Store
	current Runtime
}

func NewSettings(st *store.Store, defaults config.ReportConfig) *Settings {
	s := &Settings{
		store:   st,
		current: Normalize(fromConfig(defaults)),
	}
	_ = s.Reload(context.Background())
	return s
}

func fromConfig(c config.ReportConfig) Runtime {
	return Runtime{
		Enabled:  c.Enabled,
		SendAt:   c.SendAt,
		Timezone: c.Timezone,
		CoverDay: c.CoverDay,
		Sections: append([]string{}, c.Sections...),
	}
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
	if v, ok := get(KeySendAt); ok && strings.TrimSpace(v) != "" {
		rt.SendAt = strings.TrimSpace(v)
	}
	if v, ok := get(KeyTimezone); ok && strings.TrimSpace(v) != "" {
		rt.Timezone = strings.TrimSpace(v)
	}
	if v, ok := get(KeyCoverDay); ok && strings.TrimSpace(v) != "" {
		rt.CoverDay = strings.TrimSpace(v)
	}
	if v, ok := get(KeySections); ok {
		rt.Sections = parseSections(v)
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
	if in.SendAt != nil {
		next.SendAt = strings.TrimSpace(*in.SendAt)
	}
	if in.Timezone != nil {
		next.Timezone = strings.TrimSpace(*in.Timezone)
	}
	if in.CoverDay != nil {
		next.CoverDay = strings.TrimSpace(*in.CoverDay)
	}
	if in.Sections != nil {
		next.Sections = normalizeSections(*in.Sections)
	}
	if err := Validate(next); err != nil {
		return cur, err
	}
	next = Normalize(next)
	kv := map[string]string{
		KeyEnabled:  strconv.FormatBool(next.Enabled),
		KeySendAt:   next.SendAt,
		KeyTimezone: next.Timezone,
		KeyCoverDay: next.CoverDay,
		KeySections: sectionsToJSON(next.Sections),
	}
	if err := s.store.SetSettings(ctx, kv); err != nil {
		return cur, err
	}
	s.mu.Lock()
	s.current = next
	s.mu.Unlock()
	return Clone(next), nil
}

// Validate rejects input the operator would otherwise only notice by never
// receiving a report.
func Validate(rt Runtime) error {
	if _, _, err := ParseSendAt(rt.SendAt); err != nil {
		return err
	}
	if tz := strings.TrimSpace(rt.Timezone); tz != "" {
		if _, err := time.LoadLocation(tz); err != nil {
			return fmt.Errorf("时区无法识别：%s", tz)
		}
	}
	switch strings.TrimSpace(rt.CoverDay) {
	case "", CoverYesterday, CoverToday:
	default:
		return fmt.Errorf("统计范围只能是 yesterday 或 today")
	}
	if len(normalizeSections(rt.Sections)) == 0 {
		return fmt.Errorf("至少选择一个统计板块")
	}
	return nil
}

// Normalize clamps every field into a safe range so a bad stored value can
// never stop the scheduler.
func Normalize(rt Runtime) Runtime {
	rt.SendAt = strings.TrimSpace(rt.SendAt)
	if h, m, err := ParseSendAt(rt.SendAt); err != nil {
		rt.SendAt = "09:00"
	} else {
		rt.SendAt = fmt.Sprintf("%02d:%02d", h, m)
	}
	rt.Timezone = strings.TrimSpace(rt.Timezone)
	if rt.Timezone == "" {
		rt.Timezone = "Asia/Shanghai"
	}
	if _, err := time.LoadLocation(rt.Timezone); err != nil {
		rt.Timezone = "Asia/Shanghai"
	}
	switch strings.TrimSpace(rt.CoverDay) {
	case CoverToday:
		rt.CoverDay = CoverToday
	default:
		rt.CoverDay = CoverYesterday
	}
	rt.Sections = normalizeSections(rt.Sections)
	if len(rt.Sections) == 0 {
		rt.Sections = AllSections()
	}
	return rt
}

// HasSection reports whether a block should be rendered.
func (rt Runtime) HasSection(id string) bool {
	for _, s := range rt.Sections {
		if s == id {
			return true
		}
	}
	return false
}

// Location resolves the configured timezone, falling back to UTC.
func (rt Runtime) Location() *time.Location {
	loc, err := time.LoadLocation(rt.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

// CoverDate returns the date string the digest should describe for a given
// local wall-clock moment.
func (rt Runtime) CoverDate(nowLocal time.Time) string {
	if rt.CoverDay == CoverToday {
		return nowLocal.Format("2006-01-02")
	}
	return nowLocal.AddDate(0, 0, -1).Format("2006-01-02")
}

// ParseSendAt parses an HH:MM wall-clock string.
func ParseSendAt(v string) (int, int, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, 0, fmt.Errorf("发送时间不能为空")
	}
	parts := strings.Split(v, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("发送时间格式应为 HH:MM")
	}
	h, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	m, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil {
		return 0, 0, fmt.Errorf("发送时间格式应为 HH:MM")
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, fmt.Errorf("发送时间超出范围")
	}
	return h, m, nil
}

// Clone deep-copies a Runtime so callers cannot mutate shared state.
func Clone(rt Runtime) Runtime {
	out := rt
	out.Sections = append([]string{}, rt.Sections...)
	return out
}

func normalizeSections(in []string) []string {
	known := map[string]struct{}{}
	for _, s := range AllSections() {
		known[s] = struct{}{}
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := known[s]; !ok {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func parseSections(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []string{}
	}
	if strings.HasPrefix(raw, "[") {
		var arr []string
		if err := json.Unmarshal([]byte(raw), &arr); err == nil {
			return normalizeSections(arr)
		}
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == ';'
	})
	return normalizeSections(parts)
}

func sectionsToJSON(sections []string) string {
	b, _ := json.Marshal(normalizeSections(sections))
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
