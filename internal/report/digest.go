package report

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"sub2api-ext/internal/notify"
	"sub2api-ext/internal/store"
)

// Item is one rendered key/value line of the digest.
type Item struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// CheckinBlock is the check-in part of one day.
type CheckinBlock struct {
	Count      int64   `json:"count"`
	Amount     float64 `json:"amount"`
	PrevCount  int64   `json:"prev_count"`
	PrevAmount float64 `json:"prev_amount"`
	Budget     float64 `json:"budget"`
}

// LotteryBlock is the draw part of one day.
type LotteryBlock struct {
	Draws      int64   `json:"draws"`
	Winners    int64   `json:"winners"`
	Amount     float64 `json:"amount"`
	PrevDraws  int64   `json:"prev_draws"`
	PrevAmount float64 `json:"prev_amount"`
	Budget     float64 `json:"budget"`
	Enabled    bool    `json:"enabled"`
}

// PatrolBlock aggregates every patrol run of one day.
type PatrolBlock struct {
	Runs       int    `json:"runs"`
	Failed     int    `json:"failed_runs"`
	Checked    int    `json:"checked"`
	OK         int    `json:"ok"`
	FailedAcct int    `json:"failed_accounts"`
	Disabled   int    `json:"disabled"`
	Deleted    int    `json:"deleted"`
	Pending    int    `json:"pending"`
	Problem    int64  `json:"problem_accounts"`
	LastStatus string `json:"last_status"`
	LastAt     string `json:"last_at"`
	LastError  string `json:"last_error"`
}

// Digest is one built report, ready to render or return as JSON.
// LedgerSourceStat is one source line inside the ledger block.
type LedgerSourceStat struct {
	Source string  `json:"source"`
	Count  int64   `json:"count"`
	Amount float64 `json:"amount"`
}

// LedgerBlock aggregates extension-side credit grants for one day.
type LedgerBlock struct {
	Count      int64              `json:"count"`
	Amount     float64            `json:"amount"`
	PrevCount  int64              `json:"prev_count"`
	PrevAmount float64            `json:"prev_amount"`
	BySource   []LedgerSourceStat `json:"by_source,omitempty"`
}

type Digest struct {
	Date        string        `json:"date"`
	Timezone    string        `json:"timezone"`
	CoverDay    string        `json:"cover_day"`
	GeneratedAt time.Time     `json:"generated_at"`
	Level       string        `json:"level"`
	Title       string        `json:"title"`
	Text        string        `json:"text"`
	Items       []Item        `json:"items"`
	Checkin     *CheckinBlock `json:"checkin,omitempty"`
	Lottery     *LotteryBlock `json:"lottery,omitempty"`
	Patrol      *PatrolBlock  `json:"patrol,omitempty"`
	Ledger      *LedgerBlock  `json:"ledger,omitempty"`
}

// Event converts the digest into a deliverable notification.
func (d Digest) Event() notify.Event {
	fields := make([]notify.Field, 0, len(d.Items))
	for _, it := range d.Items {
		fields = append(fields, notify.Field{Key: it.Key, Value: it.Value})
	}
	at := d.GeneratedAt
	if at.IsZero() {
		at = time.Now()
	}
	return notify.Event{
		Type:   notify.TypeDailyReport,
		Level:  d.Level,
		Title:  d.Title,
		Text:   d.Text,
		Fields: fields,
		Time:   at,
	}
}

// PlainText renders the digest exactly as it will be delivered, so the admin
// preview shows the real message instead of an approximation.
func (d Digest) PlainText() string {
	return notify.PlainText(d.Event())
}

// patrolStats mirrors the subset of patrol.Stats persisted in stats_json.
// It is duplicated here on purpose: the report package must not depend on
// the patrol runner just to read numbers back out of SQLite.
type patrolStats struct {
	Total    int `json:"total"`
	Checked  int `json:"checked"`
	OK       int `json:"ok"`
	Failed   int `json:"failed"`
	Skipped  int `json:"skipped"`
	Enabled  int `json:"enabled"`
	Disabled int `json:"disabled"`
	Deleted  int `json:"deleted"`
	Pending  int `json:"pending"`
}

// Build assembles the digest for one covered date.
func Build(ctx context.Context, st *store.Store, rt Runtime, deps Deps, date string, now time.Time) (Digest, error) {
	loc := rt.Location()
	d := Digest{
		Date:        date,
		Timezone:    rt.Timezone,
		CoverDay:    rt.CoverDay,
		GeneratedAt: now.In(loc),
		Level:       notify.LevelInfo,
		Title:       fmt.Sprintf("Sub2API 运营日报 · %s", date),
	}
	prev := prevDate(date)
	summary := make([]string, 0, 3)

	if rt.HasSection(SectionCheckin) {
		byDate, err := st.StatsByDates(ctx, []string{date, prev})
		if err != nil {
			return d, fmt.Errorf("统计签到失败: %w", err)
		}
		today := byDate[date]
		yday := byDate[prev]
		blk := &CheckinBlock{
			Count:      today.Count,
			Amount:     today.TotalAmount,
			PrevCount:  yday.Count,
			PrevAmount: yday.TotalAmount,
		}
		if deps.CheckinBudget != nil {
			blk.Budget = deps.CheckinBudget()
		}
		d.Checkin = blk
		d.Items = append(d.Items,
			Item{Key: "签到人数", Value: fmt.Sprintf("%d 人（%s）", blk.Count, deltaInt(blk.Count, blk.PrevCount))},
			Item{Key: "签到发放", Value: budgetText(blk.Amount, blk.Budget, blk.PrevAmount)},
		)
		summary = append(summary, fmt.Sprintf("签到 %d 人/%s", blk.Count, money(blk.Amount)))
		if blk.Budget > 0 && blk.Amount >= blk.Budget {
			d.Level = notify.LevelWarn
		}
	}

	if rt.HasSection(SectionLottery) {
		byDate, err := st.LotteryStatsByDates(ctx, []string{date, prev})
		if err != nil {
			return d, fmt.Errorf("统计抽奖失败: %w", err)
		}
		today := byDate[date]
		yday := byDate[prev]
		blk := &LotteryBlock{
			Draws:      today.Draws,
			Winners:    today.Winners,
			Amount:     today.TotalAmount,
			PrevDraws:  yday.Draws,
			PrevAmount: yday.TotalAmount,
		}
		if deps.LotteryBudget != nil {
			blk.Budget = deps.LotteryBudget()
		}
		if deps.LotteryEnabled != nil {
			blk.Enabled = deps.LotteryEnabled()
		}
		d.Lottery = blk
		state := ""
		if !blk.Enabled {
			state = "（模块未开启）"
		}
		d.Items = append(d.Items,
			Item{Key: "抽奖次数", Value: fmt.Sprintf("%d 次，中奖 %d 次%s%s", blk.Draws, blk.Winners, winRate(blk.Draws, blk.Winners), state)},
			Item{Key: "抽奖发放", Value: budgetText(blk.Amount, blk.Budget, blk.PrevAmount)},
		)
		summary = append(summary, fmt.Sprintf("抽奖 %d 次/%s", blk.Draws, money(blk.Amount)))
		if blk.Budget > 0 && blk.Amount >= blk.Budget {
			d.Level = notify.LevelWarn
		}
	}

	if rt.HasSection(SectionPatrol) {
		blk, err := buildPatrol(ctx, st, date, loc)
		if err != nil {
			return d, err
		}
		d.Patrol = blk
		if blk.Runs == 0 {
			d.Items = append(d.Items, Item{Key: "账号巡检", Value: "当日没有巡检运行"})
		} else {
			d.Items = append(d.Items,
				Item{Key: "巡检运行", Value: fmt.Sprintf("%d 次（异常 %d 次），最近一次 %s %s", blk.Runs, blk.Failed, blk.LastAt, blk.LastStatus)},
				Item{Key: "巡检账号", Value: fmt.Sprintf("检测 %d，正常 %d，失败 %d，观察中 %d", blk.Checked, blk.OK, blk.FailedAcct, blk.Pending)},
			)
			if blk.Disabled > 0 || blk.Deleted > 0 {
				d.Items = append(d.Items, Item{Key: "巡检处置", Value: fmt.Sprintf("下线 %d，删除 %d", blk.Disabled, blk.Deleted)})
			}
			summary = append(summary, fmt.Sprintf("巡检 %d 次/失败 %d", blk.Runs, blk.FailedAcct))
		}
		d.Items = append(d.Items, Item{Key: "异常账号", Value: fmt.Sprintf("当前累计 %d 个连续失败账号", blk.Problem)})
		if blk.Failed > 0 || blk.Disabled > 0 || blk.Deleted > 0 || blk.Problem > 0 {
			if d.Level != notify.LevelError {
				d.Level = notify.LevelWarn
			}
		}
		if blk.LastError != "" {
			d.Items = append(d.Items, Item{Key: "巡检错误", Value: truncate(blk.LastError, 160)})
			d.Level = notify.LevelError
		}
	}

	if rt.HasSection(SectionLedger) {
		// One range query covers today + previous day.
		stats, err := st.LedgerStatsBySource(ctx, prev, date)
		if err != nil {
			return d, fmt.Errorf("统计扩展发放失败: %w", err)
		}
		blk := &LedgerBlock{BySource: make([]LedgerSourceStat, 0, len(stats))}
		for _, s := range stats {
			if s.Date == date {
				blk.Count += s.Count
				blk.Amount += s.Amount
				blk.BySource = append(blk.BySource, LedgerSourceStat{Source: s.Source, Count: s.Count, Amount: s.Amount})
				continue
			}
			if s.Date == prev {
				blk.PrevCount += s.Count
				blk.PrevAmount += s.Amount
			}
		}
		d.Ledger = blk
		d.Items = append(d.Items,
			Item{Key: "扩展发放笔数", Value: fmt.Sprintf("%d 笔（%s）", blk.Count, deltaInt(blk.Count, blk.PrevCount))},
			Item{Key: "扩展发放总额", Value: fmt.Sprintf("%s（%s）", money(blk.Amount), deltaFloat(blk.Amount, blk.PrevAmount))},
		)
		// compact per-source lines for top sources
		for _, s := range blk.BySource {
			label := s.Source
			switch s.Source {
			case "checkin":
				label = "签到入账"
			case "lottery":
				label = "抽奖入账"
			case "rank_reward":
				label = "排行发奖"
			case "task":
				label = "任务领取"
			case "inactive_reclaim":
				label = "闲置额度回收"
			case "active_redistribution":
				label = "活跃回流奖励"
			case "redistribution_compensation":
				label = "回流补偿"
			case "backfill":
				label = "历史回填"
			}
			d.Items = append(d.Items, Item{Key: "· " + label, Value: fmt.Sprintf("%d 笔 / %s", s.Count, money(s.Amount))})
		}
		summary = append(summary, fmt.Sprintf("发放 %d 笔/%s", blk.Count, money(blk.Amount)))
	}

	d.Items = append(d.Items, Item{Key: "统计范围", Value: fmt.Sprintf("%s（%s，%s）", date, CoverLabel(rt.CoverDay), rt.Timezone)})
	if len(summary) == 0 {
		d.Text = fmt.Sprintf("%s 无可用统计板块", date)
	} else {
		d.Text = strings.Join(summary, "，")
	}
	return d, nil
}

func buildPatrol(ctx context.Context, st *store.Store, date string, loc *time.Location) (*PatrolBlock, error) {
	blk := &PatrolBlock{}
	problem, err := st.CountPatrolAccountStates(ctx, true)
	if err != nil {
		return nil, fmt.Errorf("统计异常账号失败: %w", err)
	}
	blk.Problem = problem

	day, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		return nil, fmt.Errorf("日期无法解析: %w", err)
	}
	// Widen the SQL window by a day on both sides and filter precisely in Go:
	// started_at is stored as a string, so lexical range comparison is not
	// reliable across values with and without fractional seconds.
	from := day.AddDate(0, 0, -1).UTC().Format(time.RFC3339)
	to := day.AddDate(0, 0, 2).UTC().Format(time.RFC3339)
	runs, err := st.PatrolRunsBetween(ctx, from, to)
	if err != nil {
		return nil, fmt.Errorf("读取巡检运行失败: %w", err)
	}
	for _, r := range runs {
		if r.StartedAt.IsZero() || r.StartedAt.In(loc).Format("2006-01-02") != date {
			continue
		}
		blk.Runs++
		var s patrolStats
		if strings.TrimSpace(r.StatsJSON) != "" {
			_ = json.Unmarshal([]byte(r.StatsJSON), &s)
		}
		blk.Checked += s.Checked
		blk.OK += s.OK
		blk.FailedAcct += s.Failed
		blk.Disabled += s.Disabled
		blk.Deleted += s.Deleted
		blk.Pending += s.Pending
		if r.Status == "failed" || strings.TrimSpace(r.Error) != "" {
			blk.Failed++
		}
		blk.LastStatus = r.Status
		blk.LastError = strings.TrimSpace(r.Error)
		at := r.FinishedAt
		if at.IsZero() {
			at = r.StartedAt
		}
		blk.LastAt = at.In(loc).Format("15:04")
	}
	return blk, nil
}

// Deps supplies values owned by other modules without importing them.
type Deps struct {
	CheckinBudget  func() float64
	LotteryBudget  func() float64
	LotteryEnabled func() bool
}

func prevDate(date string) string {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return date
	}
	return t.AddDate(0, 0, -1).Format("2006-01-02")
}

func money(v float64) string {
	return fmt.Sprintf("%.4f", v)
}

func deltaInt(cur, prev int64) string {
	diff := cur - prev
	switch {
	case diff > 0:
		return fmt.Sprintf("较前日 +%d", diff)
	case diff < 0:
		return fmt.Sprintf("较前日 %d", diff)
	default:
		return "与前日持平"
	}
}

func deltaFloat(cur, prev float64) string {
	diff := cur - prev
	switch {
	case diff > 0.00005:
		return fmt.Sprintf("较前日 +%.4f", diff)
	case diff < -0.00005:
		return fmt.Sprintf("较前日 %.4f", diff)
	default:
		return "与前日持平"
	}
}

func budgetText(amount, budget, prevAmount float64) string {
	if budget > 0 {
		pct := amount / budget * 100
		return fmt.Sprintf("%s（预算 %s，已用 %.1f%%，%s）", money(amount), money(budget), pct, deltaFloat(amount, prevAmount))
	}
	return fmt.Sprintf("%s（预算不限，%s）", money(amount), deltaFloat(amount, prevAmount))
}

func winRate(draws, winners int64) string {
	if draws <= 0 {
		return ""
	}
	return fmt.Sprintf("（中奖率 %.1f%%）", float64(winners)/float64(draws)*100)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}
