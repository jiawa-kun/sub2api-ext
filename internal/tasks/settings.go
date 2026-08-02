package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"sub2api-ext/internal/store"
)

const SettingsKey = "tasks_settings_json"

// Def is one task definition.
type Def struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Enabled     bool    `json:"enabled"`
	Reward      float64 `json:"reward"`
	// Kind: daily_checkin | daily_lottery | streak | weekly_checkin | weekly_lottery
	Kind   string `json:"kind"`
	Target int    `json:"target"` // streak days or weekly count
	Period string `json:"period"` // daily | weekly | once
}

// Runtime is admin-configurable task pack.
type Runtime struct {
	Enabled bool  `json:"enabled"`
	Defs    []Def `json:"defs"`
}

var SupportedKinds = []string{"daily_checkin", "daily_lottery", "streak", "weekly_checkin", "weekly_lottery"}

func DefaultRuntime() Runtime {
	return Runtime{
		Enabled: true,
		Defs: []Def{
			{ID: "daily_checkin", Name: "今日签到", Description: "完成今日签到", Enabled: true, Reward: 0, Kind: "daily_checkin", Target: 1, Period: "daily"},
			{ID: "daily_lottery", Name: "今日抽奖", Description: "完成今日抽奖", Enabled: true, Reward: 0, Kind: "daily_lottery", Target: 1, Period: "daily"},
			{ID: "streak_3", Name: "连签 3 天", Description: "连续签到达到 3 天", Enabled: true, Reward: 0, Kind: "streak", Target: 3, Period: "once"},
			{ID: "streak_7", Name: "连签 7 天", Description: "连续签到达到 7 天", Enabled: true, Reward: 0, Kind: "streak", Target: 7, Period: "once"},
			{ID: "streak_14", Name: "连签 14 天", Description: "连续签到达到 14 天", Enabled: true, Reward: 0, Kind: "streak", Target: 14, Period: "once"},
			{ID: "weekly_checkin_3", Name: "本周签到 3 天", Description: "自然周内签到满 3 天", Enabled: true, Reward: 0, Kind: "weekly_checkin", Target: 3, Period: "weekly"},
			{ID: "weekly_checkin_5", Name: "本周签到 5 天", Description: "自然周内签到满 5 天", Enabled: true, Reward: 0, Kind: "weekly_checkin", Target: 5, Period: "weekly"},
			{ID: "weekly_lottery_2", Name: "本周抽奖 2 次", Description: "自然周内抽奖满 2 次", Enabled: true, Reward: 0, Kind: "weekly_lottery", Target: 2, Period: "weekly"},
			{ID: "weekly_lottery_3", Name: "本周抽奖 3 次", Description: "自然周内抽奖满 3 次", Enabled: true, Reward: 0, Kind: "weekly_lottery", Target: 3, Period: "weekly"},
		},
	}
}

// Settings holds runtime task defs with hot reload from SQLite.
type Settings struct {
	mu  sync.RWMutex
	cur Runtime
	st  *store.Store
}

func NewSettings(st *store.Store) *Settings {
	s := &Settings{st: st, cur: DefaultRuntime()}
	_ = s.Reload(context.Background())
	return s
}

func (s *Settings) Get() Runtime {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneRuntime(s.cur)
}

func (s *Settings) Reload(ctx context.Context) error {
	raw, ok, err := s.st.GetSetting(ctx, SettingsKey)
	if err != nil {
		return err
	}
	rt := DefaultRuntime()
	if ok && strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &rt); err != nil {
			return err
		}
		rt, err = normalizeRuntimeChecked(rt)
		if err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.cur = rt
	s.mu.Unlock()
	return nil
}

func (s *Settings) Save(ctx context.Context, rt Runtime) error {
	var err error
	rt, err = normalizeRuntimeChecked(rt)
	if err != nil {
		return err
	}
	b, err := json.Marshal(rt)
	if err != nil {
		return err
	}
	if err := s.st.SetSetting(ctx, SettingsKey, string(b)); err != nil {
		return err
	}
	s.mu.Lock()
	s.cur = rt
	s.mu.Unlock()
	return nil
}

func normalizeRuntimeChecked(rt Runtime) (Runtime, error) {
	known := map[string]bool{}
	for _, kind := range SupportedKinds {
		known[kind] = true
	}
	seen := map[string]bool{}
	out := Runtime{Enabled: rt.Enabled, Defs: make([]Def, 0, len(rt.Defs))}
	for i, d := range rt.Defs {
		d.ID = strings.TrimSpace(d.ID)
		d.Name = strings.TrimSpace(d.Name)
		d.Description = strings.TrimSpace(d.Description)
		d.Kind = strings.ToLower(strings.TrimSpace(d.Kind))
		d.Period = strings.ToLower(strings.TrimSpace(d.Period))
		if d.ID == "" {
			return Runtime{}, fmt.Errorf("任务 %d 缺少 ID", i+1)
		}
		if seen[d.ID] {
			return Runtime{}, fmt.Errorf("任务 ID 重复：%s", d.ID)
		}
		if !known[d.Kind] {
			return Runtime{}, fmt.Errorf("任务 %s 的条件类型不支持：%s", d.ID, d.Kind)
		}
		if d.Name == "" {
			return Runtime{}, fmt.Errorf("任务 %s 缺少名称", d.ID)
		}
		if d.Reward < 0 {
			return Runtime{}, fmt.Errorf("任务 %s 奖励不能为负数", d.ID)
		}
		if d.Target <= 0 {
			d.Target = 1
		}
		if d.Kind == "daily_checkin" || d.Kind == "daily_lottery" {
			d.Target = 1
		}
		if d.Period == "" {
			d.Period = defaultPeriod(d.Kind)
		}
		if d.Period != "daily" && d.Period != "weekly" && d.Period != "once" {
			return Runtime{}, fmt.Errorf("任务 %s 周期不支持：%s", d.ID, d.Period)
		}
		seen[d.ID] = true
		out.Defs = append(out.Defs, d)
	}
	return out, nil
}

func defaultPeriod(kind string) string {
	if kind == "streak" {
		return "once"
	}
	if strings.HasPrefix(kind, "weekly_") {
		return "weekly"
	}
	return "daily"
}

func cloneRuntime(rt Runtime) Runtime {
	out := rt
	out.Defs = append([]Def(nil), rt.Defs...)
	return out
}

// PeriodKey builds the claim period key for a task.
func PeriodKey(def Def, loc *time.Location, now time.Time) string {
	if loc == nil {
		loc = time.Local
	}
	now = now.In(loc)
	switch def.Period {
	case "weekly":
		y, w := now.ISOWeek()
		return fmt.Sprintf("%d-W%02d", y, w)
	case "once":
		return "once"
	default:
		return now.Format("2006-01-02")
	}
}

// WeekRange returns Monday..Sunday dates for the week containing now.
func WeekRange(loc *time.Location, now time.Time) (from, to string) {
	if loc == nil {
		loc = time.Local
	}
	now = now.In(loc)
	weekday := int(now.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	monday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -(weekday - 1))
	sunday := monday.AddDate(0, 0, 6)
	return monday.Format("2006-01-02"), sunday.Format("2006-01-02")
}
