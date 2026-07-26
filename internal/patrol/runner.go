package patrol

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"sub2api-ext/internal/notify"
	"sub2api-ext/internal/store"
	"sub2api-ext/internal/sub2api"
)

// Stats is one run counter snapshot.
type Stats struct {
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

// LogLine is a compact run log entry retained in SQLite.
type LogLine struct {
	Time    string `json:"time"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

// Snapshot is live/finished status for API.
type Snapshot struct {
	Running     bool      `json:"running"`
	RunID       int64     `json:"run_id,omitempty"`
	Trigger     string    `json:"trigger,omitempty"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	FinishedAt  time.Time `json:"finished_at,omitempty"`
	Status      string    `json:"status,omitempty"`
	Stats       Stats     `json:"stats"`
	Error       string    `json:"error,omitempty"`
	RecentLogs  []LogLine `json:"recent_logs,omitempty"`
	Config      Runtime   `json:"config"`
	NextCronAt  string    `json:"next_cron_hint,omitempty"`
}

type runnerState struct {
	mu         sync.Mutex
	running    bool
	stopCh     chan struct{}
	runID      int64
	trigger    string
	startedAt  time.Time
	finishedAt time.Time
	status     string
	stats      Stats
	errText    string
	logs       []LogLine
}

// Service owns scheduler + manual runs.
type Service struct {
	client   *sub2api.Client
	store    *store.Store
	settings *Settings
	notifier *notify.Notifier

	state runnerState

	schedMu   sync.Mutex
	schedStop chan struct{}
	schedWG   sync.WaitGroup
}

func NewService(client *sub2api.Client, st *store.Store, settings *Settings) *Service {
	return &Service{
		client:   client,
		store:    st,
		settings: settings,
	}
}

func (s *Service) Settings() *Settings { return s.settings }

// SetNotifier attaches an optional notifier. Safe to leave nil.
func (s *Service) SetNotifier(n *notify.Notifier) { s.notifier = n }

func (s *Service) publish(ev notify.Event) {
	if s.notifier == nil {
		return
	}
	s.notifier.Publish(ev)
}

func (s *Service) Snapshot() Snapshot {
	rt := s.settings.Get()
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	logs := append([]LogLine{}, s.state.logs...)
	return Snapshot{
		Running:    s.state.running,
		RunID:      s.state.runID,
		Trigger:    s.state.trigger,
		StartedAt:  s.state.startedAt,
		FinishedAt: s.state.finishedAt,
		Status:     s.state.status,
		Stats:      s.state.stats,
		Error:      s.state.errText,
		RecentLogs: logs,
		Config:     rt,
	}
}

// StartScheduler begins minute-ticker cron evaluation.
func (s *Service) StartScheduler() {
	s.schedMu.Lock()
	defer s.schedMu.Unlock()
	if s.schedStop != nil {
		return
	}
	s.schedStop = make(chan struct{})
	stop := s.schedStop
	s.schedWG.Add(1)
	go func() {
		defer s.schedWG.Done()
		s.cronLoop(stop)
	}()
	log.Printf("patrol scheduler started")
}

// StopScheduler stops cron loop (in-flight run continues unless StopRun).
func (s *Service) StopScheduler() {
	s.schedMu.Lock()
	stop := s.schedStop
	s.schedStop = nil
	s.schedMu.Unlock()
	if stop != nil {
		close(stop)
		s.schedWG.Wait()
	}
}

func (s *Service) cronLoop(stop <-chan struct{}) {
	// align roughly to minute boundary
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var lastFireMinute string
	for {
		select {
		case <-stop:
			return
		case now := <-ticker.C:
			rt := s.settings.Get()
			if !rt.Enabled {
				continue
			}
			expr, err := ParseCron(rt.Cron)
			if err != nil {
				continue
			}
			loc, err := time.LoadLocation(rt.Timezone)
			if err != nil {
				loc = time.Local
			}
			local := now.In(loc)
			// fire at most once per minute
			key := local.Format("2006-01-02 15:04")
			if key == lastFireMinute {
				continue
			}
			if !expr.Matches(local) {
				continue
			}
			lastFireMinute = key
			if _, err := s.Trigger(context.Background(), "cron"); err != nil {
				// already running or misconfigured is fine
				if !strings.Contains(err.Error(), "already running") {
					log.Printf("patrol cron trigger: %v", err)
				}
			}
		}
	}
}

// Trigger starts a run asynchronously. Returns run id.
func (s *Service) Trigger(ctx context.Context, trigger string) (int64, error) {
	if strings.TrimSpace(trigger) == "" {
		trigger = "manual"
	}
	rt := s.settings.Get()
	if len(rt.Groups) == 0 {
		return 0, fmt.Errorf("patrol groups is empty; configure at least one group")
	}
	if s.client.AdminToken() == "" {
		return 0, fmt.Errorf("admin credential empty; configure SUB2API_ADMIN_API_KEY first")
	}

	s.state.mu.Lock()
	if s.state.running {
		s.state.mu.Unlock()
		return 0, fmt.Errorf("already running")
	}
	s.state.running = true
	s.state.stopCh = make(chan struct{})
	s.state.trigger = trigger
	s.state.startedAt = time.Now().UTC()
	s.state.finishedAt = time.Time{}
	s.state.status = "running"
	s.state.stats = Stats{}
	s.state.errText = ""
	s.state.logs = nil
	s.state.mu.Unlock()

	runID, err := s.store.InsertPatrolRun(ctx, store.PatrolRun{
		TriggerType: trigger,
		Status:      "running",
		StartedAt:   time.Now().UTC(),
		StatsJSON:   "{}",
		LogJSON:     "[]",
	})
	if err != nil {
		s.state.mu.Lock()
		s.state.running = false
		s.state.status = "failed"
		s.state.errText = err.Error()
		s.state.mu.Unlock()
		return 0, err
	}
	s.state.mu.Lock()
	s.state.runID = runID
	s.state.mu.Unlock()

	stopCh := s.state.stopCh
	go s.execute(runID, trigger, rt, stopCh)
	return runID, nil
}

// StopRun requests cooperative stop of the current run.
func (s *Service) StopRun() bool {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	if !s.state.running || s.state.stopCh == nil {
		return false
	}
	select {
	case <-s.state.stopCh:
		// already closed
	default:
		close(s.state.stopCh)
	}
	s.appendLogLocked("warn", "收到停止请求，将在当前账号处理后结束")
	return true
}

func (s *Service) execute(runID int64, trigger string, rt Runtime, stopCh <-chan struct{}) {
	ctx := context.Background()
	var runErr error
	status := "success"
	defer func() {
		snap := s.finish(status, runErr)
		_ = s.persistRun(ctx, runID, snap)
		_ = s.store.TrimPatrolRuns(ctx, rt.KeepRuns)
		s.publishRunFinished(trigger, snap)
	}()

	s.logf("info", "开始巡检 trigger=%s groups=%v model=%q concurrency=%d action=%s fail_threshold=%d",
		trigger, rt.Groups, rt.TestModel, rt.Concurrency, rt.ActionOnFail, rt.FailThreshold)

	var accounts []sub2api.Account
	for _, group := range rt.Groups {
		if stopped(stopCh) {
			status = "stopped"
			s.logf("warn", "停止请求：拉取账号阶段中断")
			return
		}
		s.logf("info", "拉取分组账号：%s", group)
		items, err := s.client.ListAllAccounts(ctx, group, 100, rt.Timezone)
		if err != nil {
			runErr = fmt.Errorf("list group %s: %w", group, err)
			status = "failed"
			s.logf("error", "拉取分组 %s 失败：%v", group, err)
			return
		}
		s.logf("info", "分组 %s 获取 %d 个账号", group, len(items))
		accounts = append(accounts, items...)
	}

	// de-dup by id
	seen := map[int64]struct{}{}
	uniq := make([]sub2api.Account, 0, len(accounts))
	for _, a := range accounts {
		if a.ID == 0 {
			continue
		}
		if _, ok := seen[a.ID]; ok {
			continue
		}
		seen[a.ID] = struct{}{}
		uniq = append(uniq, a)
	}
	accounts = uniq
	s.setTotal(len(accounts))
	s.logf("success", "合计去重后 %d 个账号", len(accounts))

	if len(accounts) == 0 {
		s.logf("warn", "没有可巡检账号")
		return
	}

	concurrency := rt.Concurrency
	if concurrency > len(accounts) {
		concurrency = len(accounts)
	}

	var idx int64 = -1
	var wg sync.WaitGroup
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func(worker int) {
			defer wg.Done()
			for {
				if stopped(stopCh) {
					return
				}
				n := int(atomic.AddInt64(&idx, 1))
				if n >= len(accounts) {
					return
				}
				s.processAccount(ctx, accounts[n], rt, stopCh)
			}
		}(i + 1)
	}
	wg.Wait()

	if stopped(stopCh) {
		status = "stopped"
		s.logf("warn", "任务已停止")
		return
	}
	s.logf("success", "巡检完成")
}

func (s *Service) processAccount(ctx context.Context, account sub2api.Account, rt Runtime, stopCh <-chan struct{}) {
	title := fmt.Sprintf("#%d %s", account.ID, strings.TrimSpace(account.Name))
	if title == fmt.Sprintf("#%d ", account.ID) {
		title = fmt.Sprintf("#%d (未命名)", account.ID)
	}

	defer s.incChecked()

	if rt.OnlySchedulable && !account.Schedulable {
		s.incSkipped()
		s.logf("warn", "%s 已去掉调度，跳过", title)
		return
	}

	models := selectModels(account, rt)
	if len(models) == 0 {
		s.incFailed()
		s.logf("error", "%s 没有可测模型（test_model/model_mapping 为空）", title)
		s.escalate(ctx, account, title, "没有可测模型", rt)
		return
	}

	s.logf("info", "%s 开始测试 %v", title, models)
	accountOK := true
	failReason := ""
	tested := 0
	for _, model := range models {
		if stopped(stopCh) {
			break
		}
		s.logf("info", "%s 测试模型 %s", title, model)
		timeout := time.Duration(rt.TimeoutMs) * time.Millisecond
		res := s.client.TestAccountModel(ctx, account.ID, model, rt.Prompt, timeout)
		tested++
		if !res.OK {
			accountOK = false
			failReason = fmt.Sprintf("模型 %s 异常：%s", model, res.Reason)
			s.logf("error", "%s %s", title, failReason)
			if rt.StopOnFirstModelFailure {
				break
			}
			continue
		}
		s.logf("success", "%s 模型 %s 正常", title, model)
	}

	if stopped(stopCh) && tested < len(models) && accountOK {
		s.incSkipped()
		s.logf("warn", "%s 因停止请求未完成全部模型测试，未改动账号", title)
		return
	}

	if accountOK {
		s.incOK()
		if err := s.store.ResetPatrolAccountOK(ctx, account.ID, account.Name, account.EffectiveGroup()); err != nil {
			s.logf("warn", "%s 健康状态写入失败：%v", title, err)
		}
		if rt.AutoEnableOnSuccess && !account.Schedulable {
			if err := s.client.SetAccountSchedulable(ctx, account.ID, true); err != nil {
				s.logf("error", "%s 模型正常但重新启用失败：%v", title, err)
			} else {
				s.incEnabled()
				s.logf("success", "%s 全部模型正常，已重新启用 schedulable", title)
			}
		} else {
			s.logf("success", "%s 全部模型正常", title)
		}
		return
	}

	s.incFailed()
	s.escalate(ctx, account, title, failReason, rt)
}

// escalate records the failure streak and only applies ActionOnFail once the
// account has failed FailThreshold consecutive checks. This prevents a single
// upstream hiccup from disabling or deleting a healthy account.
func (s *Service) escalate(ctx context.Context, account sub2api.Account, title, reason string, rt Runtime) {
	threshold := rt.FailThreshold
	if threshold < 1 {
		threshold = 1
	}
	streak, err := s.store.UpsertPatrolAccountFail(ctx, account.ID, account.Name, account.EffectiveGroup(), reason)
	if err != nil {
		// Persistence failure must not silently escalate to a destructive action.
		s.logf("error", "%s 健康状态写入失败：%v；本次仅告警不处置", title, err)
		s.incPending()
		return
	}
	if streak < threshold {
		s.incPending()
		s.logf("warn", "%s %s（连续失败 %d/%d，未达阈值，暂不处置）", title, reason, streak, threshold)
		_ = s.store.MarkPatrolAccountAction(ctx, account.ID, "pending")
		return
	}

	s.logf("warn", "%s 连续失败 %d 次已达阈值 %d，执行 action_on_fail=%s", title, streak, threshold, rt.ActionOnFail)
	s.handleFailure(ctx, account, title, reason, rt)
}

func (s *Service) handleFailure(ctx context.Context, account sub2api.Account, title, reason string, rt Runtime) {
	switch rt.ActionOnFail {
	case ActionDelete:
		s.logf("error", "%s %s，准备删除账号", title, reason)
		if err := s.client.DeleteAccount(ctx, account.ID); err != nil {
			s.logf("error", "%s 删除失败：%v", title, err)
			return
		}
		s.incDeleted()
		_ = s.store.DeletePatrolAccountState(ctx, account.ID)
		s.logf("success", "%s 已删除账号", title)
		s.publishAccountAction(account, "删除账号", reason, notify.LevelError)
	case ActionDisable:
		s.logf("error", "%s %s，准备关闭 schedulable", title, reason)
		if err := s.client.SetAccountSchedulable(ctx, account.ID, false); err != nil {
			s.logf("error", "%s 关闭失败：%v", title, err)
			return
		}
		s.incDisabled()
		_ = s.store.MarkPatrolAccountAction(ctx, account.ID, ActionDisable)
		s.logf("success", "%s 已关闭 schedulable", title)
		s.publishAccountAction(account, "关闭调度", reason, notify.LevelWarn)
	default:
		_ = s.store.MarkPatrolAccountAction(ctx, account.ID, ActionNone)
		s.logf("warn", "%s 检测到异常但未处理账号（action_on_fail=none）：%s", title, reason)
	}
}

func selectModels(account sub2api.Account, rt Runtime) []string {
	if m := strings.TrimSpace(rt.TestModel); m != "" {
		return []string{m}
	}
	keys := account.ModelMappingKeys()
	sort.Strings(keys)
	return keys
}

func stopped(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func (s *Service) finish(status string, runErr error) Snapshot {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	s.state.running = false
	s.state.finishedAt = time.Now().UTC()
	s.state.status = status
	if runErr != nil {
		s.state.errText = runErr.Error()
		if status == "success" {
			s.state.status = "failed"
		}
	}
	logs := append([]LogLine{}, s.state.logs...)
	return Snapshot{
		Running:    false,
		RunID:      s.state.runID,
		Trigger:    s.state.trigger,
		StartedAt:  s.state.startedAt,
		FinishedAt: s.state.finishedAt,
		Status:     s.state.status,
		Stats:      s.state.stats,
		Error:      s.state.errText,
		RecentLogs: logs,
		Config:     s.settings.Get(),
	}
}

func (s *Service) persistRun(ctx context.Context, runID int64, snap Snapshot) error {
	statsRaw, _ := json.Marshal(snap.Stats)
	// keep last 200 log lines
	logs := snap.RecentLogs
	if len(logs) > 200 {
		logs = logs[len(logs)-200:]
	}
	logRaw, _ := json.Marshal(logs)
	return s.store.UpdatePatrolRun(ctx, runID, snap.Status, string(statsRaw), string(logRaw), snap.Error, snap.FinishedAt)
}

func (s *Service) logf(level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	s.state.mu.Lock()
	s.appendLogLocked(level, msg)
	s.state.mu.Unlock()
	switch level {
	case "error":
		log.Printf("[patrol] %s", msg)
	default:
		log.Printf("[patrol] %s", msg)
	}
}

func (s *Service) appendLogLocked(level, msg string) {
	line := LogLine{
		Time:    time.Now().Format("15:04:05"),
		Level:   level,
		Message: msg,
	}
	s.state.logs = append(s.state.logs, line)
	if len(s.state.logs) > 300 {
		s.state.logs = s.state.logs[len(s.state.logs)-300:]
	}
}

func (s *Service) setTotal(n int) {
	s.state.mu.Lock()
	s.state.stats.Total = n
	s.state.mu.Unlock()
}

func (s *Service) incChecked() {
	s.state.mu.Lock()
	s.state.stats.Checked++
	s.state.mu.Unlock()
}
func (s *Service) incOK() {
	s.state.mu.Lock()
	s.state.stats.OK++
	s.state.mu.Unlock()
}
func (s *Service) incFailed() {
	s.state.mu.Lock()
	s.state.stats.Failed++
	s.state.mu.Unlock()
}
func (s *Service) incSkipped() {
	s.state.mu.Lock()
	s.state.stats.Skipped++
	s.state.mu.Unlock()
}
func (s *Service) incEnabled() {
	s.state.mu.Lock()
	s.state.stats.Enabled++
	s.state.mu.Unlock()
}
func (s *Service) incDisabled() {
	s.state.mu.Lock()
	s.state.stats.Disabled++
	s.state.mu.Unlock()
}
func (s *Service) incDeleted() {
	s.state.mu.Lock()
	s.state.stats.Deleted++
	s.state.mu.Unlock()
}
func (s *Service) incPending() {
	s.state.mu.Lock()
	s.state.stats.Pending++
	s.state.mu.Unlock()
}

// AccountStates returns per-account patrol health rows from SQLite.
func (s *Service) AccountStates(ctx context.Context, onlyProblem bool, limit int) ([]store.PatrolAccountState, error) {
	return s.store.ListPatrolAccountStates(ctx, onlyProblem, limit)
}

// RecentRuns returns historical summaries from SQLite.
func (s *Service) RecentRuns(ctx context.Context, limit int) ([]map[string]any, error) {
	runs, err := s.store.ListPatrolRuns(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(runs))
	for _, r := range runs {
		var stats Stats
		_ = json.Unmarshal([]byte(r.StatsJSON), &stats)
		var logs []LogLine
		_ = json.Unmarshal([]byte(r.LogJSON), &logs)
		item := map[string]any{
			"id":           r.ID,
			"trigger":      r.TriggerType,
			"status":       r.Status,
			"started_at":   r.StartedAt,
			"finished_at":  r.FinishedAt,
			"stats":        stats,
			"error":        r.Error,
			"recent_logs":  logs,
		}
		out = append(out, item)
	}
	return out, nil
}

// publishAccountAction reports a destructive action applied to an account.
func (s *Service) publishAccountAction(account sub2api.Account, action, reason, level string) {
	if s.notifier == nil {
		return
	}
	name := strings.TrimSpace(account.Name)
	if name == "" {
		name = "(未命名)"
	}
	s.publish(notify.Event{
		Type:  notify.TypePatrolAccountAction,
		Level: level,
		Title: fmt.Sprintf("账号巡检：%s", action),
		Text:  fmt.Sprintf("账号 #%d %s 已被%s", account.ID, name, action),
		Fields: []notify.Field{
			{Key: "账号", Value: fmt.Sprintf("#%d %s", account.ID, name)},
			{Key: "分组", Value: account.EffectiveGroup()},
			{Key: "原因", Value: reason},
		},
	})
}

// publishRunFinished reports the outcome of a whole patrol run.
func (s *Service) publishRunFinished(trigger string, snap Snapshot) {
	if s.notifier == nil {
		return
	}
	st := snap.Stats
	level := notify.LevelInfo
	if st.Disabled > 0 || st.Deleted > 0 {
		level = notify.LevelWarn
	}
	if snap.Status == "failed" || snap.Error != "" {
		level = notify.LevelError
	}
	fields := []notify.Field{
		{Key: "触发方式", Value: trigger},
		{Key: "结果", Value: snap.Status},
		{Key: "账号总数", Value: strconv.Itoa(st.Total)},
		{Key: "正常", Value: strconv.Itoa(st.OK)},
		{Key: "失败", Value: strconv.Itoa(st.Failed)},
		{Key: "观察中", Value: strconv.Itoa(st.Pending)},
		{Key: "已下线", Value: strconv.Itoa(st.Disabled)},
		{Key: "已删除", Value: strconv.Itoa(st.Deleted)},
	}
	if snap.Error != "" {
		fields = append(fields, notify.Field{Key: "错误", Value: snap.Error})
	}
	s.publish(notify.Event{
		Type:   notify.TypePatrolRunFinished,
		Level:  level,
		Title:  "账号巡检完成",
		Text:   fmt.Sprintf("本次巡检 %d 个账号，失败 %d，处置 %d", st.Total, st.Failed, st.Disabled+st.Deleted),
		Fields: fields,
	})
}
