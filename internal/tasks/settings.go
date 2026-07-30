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
	Kind     string `json:"kind"`
	Target   int    `json:"target"` // streak days or weekly count
	Period   string `json:"period"` // daily | weekly | once
}

// Runtime is admin-configurable task pack.
type Runtime struct {
	Enabled bool  `json:"enabled"`
	Defs    []Def `json:"defs"`
}

func DefaultRuntime() Runtime {
	return Runtime{
		Enabled: true,
		Defs: []Def{
			{ID: "daily_checkin", Name: "今日签到", Description: "完成今日签到", Enabled: true, Reward: 0, Kind: "daily_checkin", Target: 1, Period: "daily"},
			{ID: "daily_lottery", Name: "今日抽奖", Description: "完成今日抽奖", Enabled: true, Reward: 0, Kind: "daily_lottery", Target: 1, Period: "daily"},
			{ID: "streak_3", Name: "连签 3 天", Description: "连续签到达到 3 天", Enabled: true, Reward: 0, Kind: "streak", Target: 3, Period: "once"},
			{ID: "weekly_checkin_5", Name: "本周签到 5 天", Description: "自然周内签到满 5 天", Enabled: true, Reward: 0, Kind: "weekly_checkin", Target: 5, Period: "weekly"},
			{ID: "weekly_lottery_3", Name: "本周抽奖 3 次", Description: "自然周内抽奖满 3 次", Enabled: true, Reward: 0, Kind: "weekly_lottery", Target: 3, Period: "weekly"},
		},
	}
}

// Settings holds runtime task defs with hot reload from SQLite.
type Settings struct {
	mu   sync.RWMutex
	cur  Runtime
	st   *store.Store
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
		// ensure known ids still present
		rt = mergeDefaults(rt)
	}
	s.mu.Lock()
	s.cur = rt
	s.mu.Unlock()
	return nil
}

func (s *Settings) Save(ctx context.Context, rt Runtime) error {
	rt = mergeDefaults(rt)
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

func mergeDefaults(rt Runtime) Runtime {
	def := DefaultRuntime()
	byID := map[string]Def{}
	for _, d := range def.Defs {
		byID[d.ID] = d
	}
	for _, d := range rt.Defs {
		if d.ID == "" {
			continue
		}
		base := byID[d.ID]
		if base.ID == "" {
			byID[d.ID] = d
			continue
		}
		base.Name = first(d.Name, base.Name)
		base.Description = first(d.Description, base.Description)
		base.Enabled = d.Enabled
		base.Reward = d.Reward
		if d.Target > 0 {
			base.Target = d.Target
		}
		byID[d.ID] = base
	}
	out := Runtime{Enabled: rt.Enabled, Defs: make([]Def, 0, len(def.Defs))}
	// keep default order first
	seen := map[string]bool{}
	for _, d := range def.Defs {
		out.Defs = append(out.Defs, byID[d.ID])
		seen[d.ID] = true
	}
	for id, d := range byID {
		if !seen[id] {
			out.Defs = append(out.Defs, d)
		}
	}
	return out
}

func first(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
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
