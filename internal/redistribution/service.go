package redistribution

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"sub2api-ext/internal/credit"
	"sub2api-ext/internal/notify"
	"sub2api-ext/internal/patrol"
	"sub2api-ext/internal/store"
	"sub2api-ext/internal/sub2api"
)

type Stats struct {
	Running       bool    `json:"running"`
	RunBatchID    int64   `json:"run_batch_id,omitempty"`
	Previewed     int64   `json:"previewed"`
	Executed      int64   `json:"executed"`
	Failed        int64   `json:"failed"`
	AvailablePool float64 `json:"available_pool"`
	LastBatchID   int64   `json:"last_batch_id,omitempty"`
	LastStatus    string  `json:"last_status,omitempty"`
	LastError     string  `json:"last_error,omitempty"`
	NextDueAt     string  `json:"next_due_at,omitempty"`
}

type DrawResult struct {
	Draw          store.RedistributionPoolDraw `json:"draw"`
	AvailablePool float64                      `json:"available_pool"`
}

type PoolResult struct {
	AvailablePool float64                       `json:"available_pool"`
	TodayDraw     *store.RedistributionPoolDraw `json:"today_draw,omitempty"`
	Recoverable   []store.RedistributionPoolLot `json:"recoverable"`
	Rules         PublicPoolRules               `json:"rules"`
}

type PublicPoolRules struct {
	WorkdayDefinition      string       `json:"workday_definition"`
	EffectiveAt            string       `json:"effective_at,omitempty"`
	InactiveLogic          string       `json:"inactive_logic"`
	InactiveRules          []PublicRule `json:"inactive_rules"`
	ActiveLogic            string       `json:"active_logic"`
	ActiveRules            []PublicRule `json:"active_rules"`
	PaidBalanceProtected   bool         `json:"paid_balance_protected"`
	NewUserProtectionDays  int          `json:"new_user_protection_days"`
	ReclaimMode            string       `json:"reclaim_mode"`
	ReclaimValue           float64      `json:"reclaim_value"`
	ReclaimMinBalance      float64      `json:"reclaim_min_balance"`
	ReclaimReserveBalance  float64      `json:"reclaim_reserve_balance"`
	ReclaimMinPerUser      float64      `json:"reclaim_min_per_user"`
	ReclaimMaxPerUser      float64      `json:"reclaim_max_per_user"`
	DrawFrequency          string       `json:"draw_frequency"`
	DrawIncludesWeekends   bool         `json:"draw_includes_weekends"`
	DrawMode               string       `json:"draw_mode"`
	DrawFixedAmount        float64      `json:"draw_fixed_amount,omitempty"`
	DrawMinAmount          float64      `json:"draw_min_amount,omitempty"`
	DrawMaxAmount          float64      `json:"draw_max_amount,omitempty"`
	ActiveShareMode        string       `json:"active_share_mode,omitempty"`
	PoolExpireDays         int          `json:"pool_expire_days"`
	CanRecoverBeforeExpiry bool         `json:"can_recover_before_expiry"`
}

type PublicRule struct {
	Type string `json:"type"`
	Days int    `json:"days"`
}

func publicPoolRules(rt Runtime, effectiveAt string) PublicPoolRules {
	return PublicPoolRules{
		WorkdayDefinition:      "周一至周五计入工作日；周末不计天数，但周末活动仍有效",
		EffectiveAt:            effectiveAt,
		InactiveLogic:          rt.InactiveLogic,
		InactiveRules:          publicRules(rt.InactiveRules),
		ActiveLogic:            rt.ActiveLogic,
		ActiveRules:            publicRules(rt.ActiveRules),
		PaidBalanceProtected:   true,
		NewUserProtectionDays:  rt.NewUserProtectionDays,
		ReclaimMode:            rt.Reclaim.Mode,
		ReclaimValue:           rt.Reclaim.Value,
		ReclaimMinBalance:      rt.Reclaim.MinBalance,
		ReclaimReserveBalance:  rt.Reclaim.ReserveBalance,
		ReclaimMinPerUser:      rt.Reclaim.MinPerUser,
		ReclaimMaxPerUser:      rt.Reclaim.MaxPerUser,
		DrawFrequency:          "daily",
		DrawIncludesWeekends:   true,
		DrawMode:               rt.DrawMode,
		DrawFixedAmount:        rt.DrawFixedAmount,
		DrawMinAmount:          rt.DrawMinAmount,
		DrawMaxAmount:          rt.DrawMaxAmount,
		ActiveShareMode:        rt.ActiveShareMode,
		PoolExpireDays:         rt.PoolExpireDays,
		CanRecoverBeforeExpiry: true,
	}
}

func publicRules(rules []Rule) []PublicRule {
	out := make([]PublicRule, 0, len(rules))
	for _, rule := range rules {
		if rule.Enabled {
			out = append(out, PublicRule{Type: rule.Type, Days: rule.Days})
		}
	}
	return out
}

type BatchDetail struct {
	Batch   store.RedistributionBatch   `json:"batch"`
	Donors  []store.RedistributionEntry `json:"donors"`
	Rewards []store.RedistributionEntry `json:"rewards"`
}

type Service struct {
	store    *store.Store
	client   *sub2api.Client
	credit   *credit.Service
	settings *Settings
	notifier *notify.Notifier

	mu          sync.Mutex
	running     bool
	runBatchID  int64
	stopCh      chan struct{}
	previewed   int64
	executed    int64
	failed      int64
	lastBatchID int64
	lastStatus  string
	lastError   string

	schedMu   sync.Mutex
	schedStop chan struct{}
	schedWG   sync.WaitGroup
}

func NewService(st *store.Store, client *sub2api.Client, creditSvc *credit.Service, settings *Settings, notifier *notify.Notifier) *Service {
	return &Service{store: st, client: client, credit: creditSvc, settings: settings, notifier: notifier}
}

func (s *Service) Settings() *Settings { return s.settings }

func (s *Service) Preview(ctx context.Context, trigger string, now time.Time) (BatchDetail, error) {
	if strings.TrimSpace(trigger) == "" {
		trigger = "manual"
	}
	rt := s.settings.Get()
	detail, err := s.buildPlan(ctx, rt, trigger, now)
	if err != nil {
		s.recordError(err)
		return BatchDetail{}, err
	}
	s.mu.Lock()
	s.previewed++
	s.lastBatchID = detail.Batch.ID
	s.lastStatus = detail.Batch.Status
	s.lastError = ""
	s.mu.Unlock()
	return detail, nil
}

func (s *Service) buildPlan(ctx context.Context, rt Runtime, trigger string, now time.Time) (BatchDetail, error) {
	loc, err := time.LoadLocation(rt.Timezone)
	if err != nil {
		loc = time.Local
	}
	localNow := now.In(loc)
	users, err := s.client.ListAllAdminUsers(ctx, rt.MaxUsers)
	if err != nil {
		return BatchDetail{}, fmt.Errorf("加载用户失败: %w", err)
	}
	userIDs := make([]int64, 0, len(users))
	for _, user := range users {
		userIDs = append(userIDs, user.ID)
	}
	usage, err := s.client.BatchUserUsage(ctx, userIDs)
	if err != nil {
		return BatchDetail{}, fmt.Errorf("加载用户消费失败: %w", err)
	}
	extension, err := s.store.LatestExtensionActivity(ctx)
	if err != nil {
		return BatchDetail{}, fmt.Errorf("加载扩展活跃记录失败: %w", err)
	}
	recentUsage := s.loadRecentUsage(ctx, rt, localNow)
	carry, err := s.store.RedistributionAvailablePool(ctx)
	if err != nil {
		return BatchDetail{}, fmt.Errorf("读取回流池失败: %w", err)
	}
	if carry < 0 {
		carry = 0
	}

	snapshots := make([]UserSnapshot, 0, len(users))
	for _, user := range users {
		var extAt *time.Time
		if t, ok := extension[user.ID]; ok {
			t2 := t
			extAt = &t2
		}
		stat := usage[user.ID]
		snapshots = append(snapshots, UserSnapshot{
			User: user, ExtensionAt: extAt, TotalUsage: stat.TotalActualCost, RecentUsage: recentUsage[user.ID],
		})
	}

	// Deterministic order: the stalest users are reclaimed first when a batch cap applies.
	sort.SliceStable(snapshots, func(i, j int) bool {
		ai := activitySortTime(snapshots[i])
		aj := activitySortTime(snapshots[j])
		if ai.Equal(aj) {
			return snapshots[i].User.ID < snapshots[j].User.ID
		}
		return ai.Before(aj)
	})

	donorEntries := []store.RedistributionEntry{}
	plannedReclaim := 0.0
	for _, snap := range snapshots {
		ok, reasons := IsInactive(rt, snap, localNow)
		if !ok {
			continue
		}
		amount := ReclaimAmount(rt.Reclaim, snap.User.Balance)
		if amount <= 0 {
			continue
		}
		if rt.Reclaim.BatchCap > 0 {
			remaining := floorMoney(rt.Reclaim.BatchCap - plannedReclaim)
			if remaining <= 0 {
				break
			}
			if amount > remaining {
				amount = remaining
			}
			if amount < rt.Reclaim.MinPerUser {
				break
			}
		}
		plannedReclaim += amount
		donorEntries = append(donorEntries, store.RedistributionEntry{
			UserID: snap.User.ID, Role: store.RedistributionRoleDonor,
			DisplayName: displayName(snap.User), BalanceBefore: snap.User.Balance,
			LastActiveAt: snap.User.LastActiveAt, LastUsedAt: snap.User.LastUsedAt,
			ExtensionAt: snap.ExtensionAt, UsageAmount: snap.RecentUsage,
			PlannedAmount: amount, Status: store.RedistributionEntryPlanned,
			Reason: strings.Join(reasons, "；"),
		})
	}

	rewardEntries := []store.RedistributionEntry{}
	plannedDistribute := 0.0
	raw, _ := json.Marshal(rt)
	periodKey := localNow.Format("2006-01-02")
	batch := store.RedistributionBatch{
		TriggerType: trigger, PeriodKey: periodKey, Status: store.RedistributionBatchDraft,
		ConfigJSON: string(raw), CandidateCount: len(donorEntries), RecipientCount: 0,
		PlannedReclaim: floorMoney(plannedReclaim), CarryIn: floorMoney(carry),
		PlannedDistribute: floorMoney(plannedDistribute), CreatedAt: now.UTC(),
	}
	entries := append(append([]store.RedistributionEntry{}, donorEntries...), rewardEntries...)
	batchID, err := s.store.CreateRedistributionBatch(ctx, batch, entries)
	if err != nil {
		return BatchDetail{}, err
	}
	batch.ID = batchID
	for i := range donorEntries {
		donorEntries[i].BatchID = batchID
	}
	for i := range rewardEntries {
		rewardEntries[i].BatchID = batchID
	}
	// Re-read to include generated entry IDs.
	all, err := s.store.ListRedistributionEntries(ctx, batchID, "")
	if err == nil {
		donorEntries, rewardEntries = splitEntries(all)
	}
	return BatchDetail{Batch: batch, Donors: donorEntries, Rewards: rewardEntries}, nil
}

func (s *Service) Execute(ctx context.Context, batchID int64) (BatchDetail, error) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return BatchDetail{}, fmt.Errorf("已有回流批次正在执行")
	}
	s.running = true
	s.runBatchID = batchID
	s.stopCh = make(chan struct{})
	stop := s.stopCh
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.running = false
		s.runBatchID = 0
		s.stopCh = nil
		s.mu.Unlock()
	}()

	batch, err := s.store.GetRedistributionBatch(ctx, batchID)
	if err != nil {
		s.recordExecutionFailure(batchID, err)
		return BatchDetail{}, err
	}
	var rt Runtime
	if err := json.Unmarshal([]byte(batch.ConfigJSON), &rt); err != nil {
		s.recordExecutionFailure(batchID, err)
		return BatchDetail{}, fmt.Errorf("批次配置损坏: %w", err)
	}
	rt = normalizeRuntime(rt)
	if !rt.Enabled {
		return BatchDetail{}, fmt.Errorf("额度回流功能未启用")
	}
	if err := s.store.MarkRedistributionBatchRunning(ctx, batchID); err != nil {
		return BatchDetail{}, err
	}
	entries, err := s.store.ListRedistributionEntries(ctx, batchID, "")
	if err != nil {
		return BatchDetail{}, err
	}
	donors, _ := splitEntries(entries)
	carry, _ := s.store.RedistributionAvailablePool(ctx)
	if carry < 0 {
		carry = 0
	}
	batch.CarryIn = floorMoney(carry)
	batch.Status = store.RedistributionBatchRunning
	actualReclaim := 0.0
	donorFailures := 0
	stoppedByFailure := false

	for i := range donors {
		if stopped(stop) {
			batch.Status = store.RedistributionBatchStopped
			break
		}
		entry := &donors[i]
		user, err := s.client.GetUserByAdmin(ctx, entry.UserID)
		if err != nil {
			entry.Status = store.RedistributionEntryFailed
			entry.Error = err.Error()
			donorFailures++
			_ = s.store.UpdateRedistributionEntry(ctx, *entry)
			if failureExceeded(rt, donorFailures, len(donors)) {
				stoppedByFailure = true
				break
			}
			continue
		}
		amount := ReclaimAmount(rt.Reclaim, user.Balance)
		if amount > entry.PlannedAmount {
			amount = entry.PlannedAmount
		}
		amount = floorMoney(amount)
		entry.BalanceBefore = user.Balance
		entry.PlannedAmount = amount
		if amount <= 0 {
			entry.Status = store.RedistributionEntrySkipped
			entry.Reason += "；执行时余额不足"
			_ = s.store.UpdateRedistributionEntry(ctx, *entry)
			continue
		}
		entry.Status = store.RedistributionEntryProcessing
		entry.IdempotencyKey = sub2api.IdempotencyKey("inactive-reclaim", entry.UserID, fmt.Sprintf("batch-%d", batchID))
		_ = s.store.UpdateRedistributionEntry(ctx, *entry)
		res, err := s.credit.Reclaim(ctx, credit.Request{
			UserID: entry.UserID, Amount: amount, Source: credit.SourceInactiveReclaim,
			SourceRef: fmt.Sprintf("redistribution:%d", batchID), Scope: "inactive-reclaim",
			Slot: fmt.Sprintf("batch-%d", batchID), Notes: fmt.Sprintf("inactive-reclaim batch=%d", batchID),
			IdempotencyKey: entry.IdempotencyKey,
		})
		if err != nil {
			entry.Status = store.RedistributionEntryFailed
			entry.Error = err.Error()
			donorFailures++
		} else {
			entry.Status = store.RedistributionEntrySuccess
			entry.ActualAmount = amount
			entry.LedgerID = res.LedgerID
			entry.BalanceAfter = res.NewBalance
			actualReclaim += amount
		}
		_ = s.store.UpdateRedistributionEntry(ctx, *entry)
		if failureExceeded(rt, donorFailures, len(donors)) {
			stoppedByFailure = true
			break
		}
	}
	batch.ActualReclaim = floorMoney(actualReclaim)
	if stoppedByFailure {
		batch.Status = store.RedistributionBatchFailed
		batch.Error = "回收失败比例达到熔断阈值，已停止发放"
	}
	if batch.Status == store.RedistributionBatchStopped || batch.Status == store.RedistributionBatchFailed {
		_ = s.store.FinishRedistributionBatch(ctx, *batch)
		s.recordExecutionFailure(batchID, fmt.Errorf("%s", batch.Error))
		return s.Detail(ctx, batchID)
	}

	if batch.ActualReclaim > 0 {
		expires := time.Now().UTC().AddDate(0, 0, rt.PoolExpireDays)
		for _, entry := range donors {
			if entry.Status != store.RedistributionEntrySuccess || entry.ActualAmount <= 0 {
				continue
			}
			if _, err := s.store.CreateRedistributionPoolLot(ctx, store.RedistributionPoolLot{SourceBatchID: batchID, SourceUserID: entry.UserID, OriginalAmount: entry.ActualAmount, Status: store.PoolLotAvailable, CreatedAt: time.Now().UTC(), ExpiresAt: expires}); err != nil {
				return BatchDetail{}, fmt.Errorf("创建回流资金份额失败: %w", err)
			}
		}
	}
	batch.PlannedDistribute = 0
	batch.ActualDistribute = 0
	batch.RecipientCount = 0
	if donorFailures > 0 {
		batch.Status = store.RedistributionBatchPartial
	} else {
		batch.Status = store.RedistributionBatchSuccess
	}
	if err := s.store.FinishRedistributionBatch(ctx, *batch); err != nil {
		return BatchDetail{}, err
	}
	_ = s.store.TrimRedistributionBatches(ctx, rt.KeepBatches)
	s.mu.Lock()
	s.executed++
	s.lastBatchID = batchID
	s.lastStatus = batch.Status
	s.lastError = ""
	s.mu.Unlock()
	s.publishFinished(*batch, donorFailures, 0)
	return s.Detail(ctx, batchID)
}

func (s *Service) Detail(ctx context.Context, batchID int64) (BatchDetail, error) {
	batch, err := s.store.GetRedistributionBatch(ctx, batchID)
	if err != nil {
		return BatchDetail{}, err
	}
	entries, err := s.store.ListRedistributionEntries(ctx, batchID, "")
	if err != nil {
		return BatchDetail{}, err
	}
	donors, rewards := splitEntries(entries)
	return BatchDetail{Batch: *batch, Donors: donors, Rewards: rewards}, nil
}

func (s *Service) Claim(ctx context.Context, userID, batchID int64) (store.RedistributionEntry, error) {
	if err := s.store.ExpireRedistributionEntitlements(ctx, time.Now().UTC()); err != nil {
		return store.RedistributionEntry{}, err
	}
	entry, err := s.store.GetRedistributionEntry(ctx, batchID, userID, store.RedistributionRoleRecipient)
	if err != nil {
		return store.RedistributionEntry{}, err
	}
	if entry.Status == store.RedistributionEntryClaimed || entry.Status == store.RedistributionEntrySuccess {
		return *entry, nil
	}
	if entry.Status != store.RedistributionEntryPending {
		return store.RedistributionEntry{}, fmt.Errorf("奖励当前不可领取")
	}
	if entry.ExpiresAt != nil && time.Now().After(*entry.ExpiresAt) {
		return store.RedistributionEntry{}, fmt.Errorf("奖励已过期")
	}
	if err := s.store.MarkRedistributionClaimProcessing(ctx, entry.ID); err != nil {
		return store.RedistributionEntry{}, err
	}
	res, err := s.credit.Grant(ctx, credit.Request{
		UserID: userID, Amount: entry.PlannedAmount, Source: credit.SourceRedistribution,
		SourceRef: fmt.Sprintf("redistribution:%d", batchID), Scope: "redistribution",
		Slot: fmt.Sprintf("batch-%d", batchID), Notes: fmt.Sprintf("active-redistribution claim batch=%d", batchID),
		IdempotencyKey: entry.IdempotencyKey,
	})
	if err != nil {
		_ = s.store.ResetRedistributionClaim(ctx, entry.ID, err.Error())
		return store.RedistributionEntry{}, err
	}
	entry.Status = store.RedistributionEntryClaimed
	entry.ActualAmount = entry.PlannedAmount
	entry.LedgerID = res.LedgerID
	entry.BalanceAfter = res.NewBalance
	if err := s.store.CompleteRedistributionClaim(ctx, *entry); err != nil {
		return store.RedistributionEntry{}, err
	}
	return *entry, nil
}

func (s *Service) Pool(ctx context.Context, userID int64, now time.Time) (PoolResult, error) {
	rt := s.settings.Get()
	loc, err := time.LoadLocation(rt.Timezone)
	if err != nil {
		loc = time.Local
	}
	local := now.In(loc)
	drawDate := local.Format("2006-01-02")
	draw, err := s.store.GetRedistributionPoolDraw(ctx, userID, drawDate)
	if err != nil && err != sql.ErrNoRows {
		return PoolResult{}, err
	}
	lots, err := s.store.ListRedistributionPoolLots(ctx, userID, false, 100)
	if err != nil {
		return PoolResult{}, err
	}
	validLots := lots[:0]
	for _, lot := range lots {
		if lot.ExpiresAt.IsZero() || lot.ExpiresAt.After(now.UTC()) {
			validLots = append(validLots, lot)
		}
	}
	return PoolResult{
		AvailablePool: func() float64 { v, _ := s.store.RedistributionAvailablePool(ctx); return v }(),
		TodayDraw:     draw,
		Recoverable:   validLots,
		Rules:         publicPoolRules(rt, s.settings.UpdatedAt()),
	}, nil
}

func (s *Service) Draw(ctx context.Context, userID int64, now time.Time) (DrawResult, error) {
	if userID <= 0 {
		return DrawResult{}, fmt.Errorf("user id required")
	}
	rt := s.settings.Get()
	if !rt.Enabled {
		return DrawResult{}, fmt.Errorf("额度回流功能未启用")
	}
	loc, err := time.LoadLocation(rt.Timezone)
	if err != nil {
		loc = time.Local
	}
	local := now.In(loc)
	drawDate := local.Format("2006-01-02")
	var reserved *store.RedistributionPoolDraw
	if existing, err := s.store.GetRedistributionPoolDraw(ctx, userID, drawDate); err == nil {
		if existing.Status == store.PoolDrawSuccess {
			return DrawResult{Draw: *existing, AvailablePool: s.pool(ctx)}, nil
		}
		if existing.Status == store.PoolDrawProcessing {
			reserved = existing
		}
	} else if err != sql.ErrNoRows {
		return DrawResult{}, err
	}
	if reserved != nil {
		grant, err := s.credit.Grant(ctx, credit.Request{UserID: userID, Amount: reserved.Amount, Source: credit.SourceRedistribution, SourceRef: "redistribution-pool:" + drawDate, Scope: "redistribution-pool", Slot: drawDate, Notes: "redistribution pool draw", IdempotencyKey: reserved.IdempotencyKey})
		if err != nil {
			_ = s.store.ResetRedistributionPoolDraw(ctx, reserved.ID, err.Error())
			return DrawResult{}, err
		}
		if err := s.store.CompleteRedistributionPoolDraw(ctx, reserved.ID, grant.LedgerID, ""); err != nil {
			return DrawResult{}, err
		}
		reserved.Status = store.PoolDrawSuccess
		reserved.LedgerID = grant.LedgerID
		return DrawResult{Draw: *reserved, AvailablePool: s.pool(ctx)}, nil
	}
	if _, err := s.client.GetUserByAdmin(ctx, userID); err != nil {
		return DrawResult{}, err
	}
	users, err := s.client.ListAllAdminUsers(ctx, rt.MaxUsers)
	if err != nil {
		return DrawResult{}, err
	}
	recentUsage := s.loadRecentUsage(ctx, rt, local)
	active := make([]UserSnapshot, 0, len(users))
	drawn, err := s.store.ListRedistributionPoolDrawUsers(ctx, drawDate)
	if err != nil {
		return DrawResult{}, err
	}
	extension, err := s.store.LatestExtensionActivity(ctx)
	if err != nil {
		return DrawResult{}, err
	}
	for _, candidate := range users {
		if drawn[candidate.ID] {
			continue
		}
		t := extension[candidate.ID]
		var ext *time.Time
		if !t.IsZero() {
			ext = &t
		}
		snap := UserSnapshot{User: candidate, ExtensionAt: ext, RecentUsage: recentUsage[candidate.ID]}
		if inactive, _ := IsInactive(rt, snap, local); inactive {
			continue
		}
		if ok, _ := IsActive(rt, snap, local); ok {
			active = append(active, snap)
		}
	}
	if len(active) == 0 {
		return DrawResult{}, fmt.Errorf("当前没有符合条件的活跃用户")
	}
	pool := s.pool(ctx)
	if pool <= 0 {
		return DrawResult{}, fmt.Errorf("回流池暂无可抽取额度")
	}
	amount, err := drawAmount(rt, pool, active, userID)
	if err != nil {
		return DrawResult{}, err
	}
	if amount <= 0 {
		return DrawResult{}, fmt.Errorf("当前可抽取额度不足")
	}
	if reserved == nil {
		draw := store.RedistributionPoolDraw{UserID: userID, DrawDate: drawDate, Mode: rt.DrawMode, Amount: amount, IdempotencyKey: sub2api.IdempotencyKey("redistribution-pool-draw", userID, drawDate)}
		reserved, err = s.store.ReserveRedistributionPoolDraw(ctx, draw, now.UTC())
		if err != nil {
			return DrawResult{}, err
		}
	}
	grant, err := s.credit.Grant(ctx, credit.Request{UserID: userID, Amount: reserved.Amount, Source: credit.SourceRedistribution, SourceRef: "redistribution-pool:" + drawDate, Scope: "redistribution-pool", Slot: drawDate, Notes: "redistribution pool draw", IdempotencyKey: reserved.IdempotencyKey})
	if err != nil {
		_ = s.store.ResetRedistributionPoolDraw(ctx, reserved.ID, err.Error())
		return DrawResult{}, err
	}
	if err := s.store.CompleteRedistributionPoolDraw(ctx, reserved.ID, grant.LedgerID, ""); err != nil {
		return DrawResult{}, err
	}
	reserved.Status = store.PoolDrawSuccess
	reserved.LedgerID = grant.LedgerID
	return DrawResult{Draw: *reserved, AvailablePool: s.pool(ctx)}, nil
}

func (s *Service) Recover(ctx context.Context, userID int64, now time.Time) ([]store.RedistributionPoolRefund, error) {
	if userID <= 0 {
		return nil, fmt.Errorf("user id required")
	}
	lots, err := s.store.ListRedistributionPoolLots(ctx, userID, false, 100)
	if err != nil {
		return nil, err
	}
	if len(lots) == 0 {
		return nil, fmt.Errorf("暂无可追回额度")
	}
	refunded := []store.RedistributionPoolRefund{}
	for _, lot := range lots {
		if !lot.ExpiresAt.IsZero() && !lot.ExpiresAt.After(now) {
			continue
		}
		refund, err := s.store.ReserveRedistributionPoolRefund(ctx, lot.ID, store.PoolRefundManual, now.UTC())
		if err != nil {
			return refunded, err
		}
		grant, err := s.credit.Grant(ctx, credit.Request{UserID: userID, Amount: refund.Amount, Source: credit.SourceRedistributionCompensation, SourceRef: fmt.Sprintf("redistribution-pool-lot:%d", lot.ID), Scope: "redistribution-pool-refund", Slot: fmt.Sprintf("lot-%d", lot.ID), Notes: "manual redistribution pool recovery", IdempotencyKey: refund.IdempotencyKey})
		if err != nil {
			_ = s.store.ResetRedistributionPoolRefund(ctx, refund.ID, err.Error())
			return refunded, err
		}
		if err := s.store.CompleteRedistributionPoolRefund(ctx, refund.ID, grant.LedgerID); err != nil {
			return refunded, err
		}
		refund.Status = store.PoolRefundSuccess
		refund.LedgerID = grant.LedgerID
		refunded = append(refunded, *refund)
	}
	return refunded, nil
}

func (s *Service) RefundExpired(ctx context.Context, now time.Time) error {
	lots, err := s.store.ListExpiredRedistributionPoolLots(ctx, now.UTC(), 100)
	if err != nil {
		return err
	}
	for _, lot := range lots {
		refund, err := s.store.ReserveRedistributionPoolRefund(ctx, lot.ID, store.PoolRefundAuto, now.UTC())
		if err != nil {
			continue
		}
		grant, err := s.credit.Grant(ctx, credit.Request{UserID: lot.SourceUserID, Amount: refund.Amount, Source: credit.SourceRedistributionCompensation, SourceRef: fmt.Sprintf("redistribution-pool-lot:%d", lot.ID), Scope: "redistribution-pool-refund", Slot: fmt.Sprintf("lot-%d", lot.ID), Notes: "expired redistribution pool refund", IdempotencyKey: refund.IdempotencyKey})
		if err != nil {
			_ = s.store.ResetRedistributionPoolRefund(ctx, refund.ID, err.Error())
			continue
		}
		if err := s.store.CompleteRedistributionPoolRefund(ctx, refund.ID, grant.LedgerID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) pool(ctx context.Context) float64 {
	v, _ := s.store.RedistributionAvailablePool(ctx)
	return floorMoney(v)
}

func drawAmount(rt Runtime, pool float64, users []UserSnapshot, userID int64) (float64, error) {
	switch rt.DrawMode {
	case DrawFixed:
		if pool < rt.DrawFixedAmount {
			return 0, fmt.Errorf("回流池余额不足固定抽取金额")
		}
		return floorMoney(rt.DrawFixedAmount), nil
	case DrawRandom:
		max := rt.DrawMaxAmount
		if max > pool {
			max = pool
		}
		if max < rt.DrawMinAmount {
			return 0, fmt.Errorf("回流池余额不足随机抽取下限")
		}
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return 0, err
		}
		var n uint64
		for _, v := range b {
			n = n<<8 | uint64(v)
		}
		ratio := float64(n) / float64(^uint64(0))
		return floorMoney(rt.DrawMinAmount + ratio*(max-rt.DrawMinAmount)), nil
	case DrawActiveShare:
		if rt.ActiveShareMode == ActiveShareUsage {
			total := 0.0
			current := 0.0
			for _, u := range users {
				if u.RecentUsage > 0 {
					total += u.RecentUsage
				}
				if u.User.ID == userID {
					current = u.RecentUsage
				}
			}
			if current <= 0 || total <= 0 {
				return 0, fmt.Errorf("当前用户没有可用于占比计算的消费")
			}
			return floorMoney(pool * current / total), nil
		}
		return floorMoney(pool / float64(len(users))), nil
	default:
		return 0, fmt.Errorf("未知抽取模式")
	}
}

func (s *Service) Stop() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running || s.stopCh == nil {
		return false
	}
	select {
	case <-s.stopCh:
	default:
		close(s.stopCh)
	}
	return true
}

func (s *Service) Stats(ctx context.Context) Stats {
	s.mu.Lock()
	out := Stats{Running: s.running, RunBatchID: s.runBatchID, Previewed: s.previewed,
		Executed: s.executed, Failed: s.failed, LastBatchID: s.lastBatchID,
		LastStatus: s.lastStatus, LastError: s.lastError}
	s.mu.Unlock()
	out.AvailablePool, _ = s.store.RedistributionAvailablePool(ctx)
	rt := s.settings.Get()
	if rt.AutoExecute && rt.Enabled {
		if next := nextDue(rt, time.Now()); !next.IsZero() {
			out.NextDueAt = next.Format("2006-01-02 15:04")
		}
	}
	return out
}

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
		s.schedulerLoop(stop)
	}()
	log.Printf("redistribution scheduler started")
}

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

func (s *Service) schedulerLoop(stop <-chan struct{}) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	lastMinute := ""
	lastRefundMinute := ""
	for {
		select {
		case <-stop:
			return
		case now := <-ticker.C:
			rt := s.settings.Get()
			if !rt.Enabled {
				continue
			}
			refundMinute := now.UTC().Format("2006-01-02 15:04")
			if refundMinute != lastRefundMinute {
				lastRefundMinute = refundMinute
				go func(at time.Time) {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
					defer cancel()
					if err := s.RefundExpired(ctx, at); err != nil {
						log.Printf("redistribution expired refund failed: %v", err)
					}
				}(now)
			}
			if !rt.AutoExecute {
				continue
			}
			loc, err := time.LoadLocation(rt.Timezone)
			if err != nil {
				continue
			}
			local := now.In(loc)
			minute := local.Format("2006-01-02 15:04")
			if minute == lastMinute {
				continue
			}
			lastMinute = minute
			expr, err := patrol.ParseCron(rt.Cron)
			if err != nil || !expr.Matches(local) {
				continue
			}
			period := local.Format("2006-01-02")
			if done, _ := s.store.HasScheduledRedistributionBatch(context.Background(), period); done {
				continue
			}
			go s.runScheduled(now)
		}
	}
}

func (s *Service) runScheduled(now time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	detail, err := s.Preview(ctx, "schedule", now)
	if err != nil {
		log.Printf("redistribution scheduled preview failed: %v", err)
		return
	}
	if _, err := s.Execute(ctx, detail.Batch.ID); err != nil {
		log.Printf("redistribution scheduled execute failed: %v", err)
	}
}

func (s *Service) loadRecentUsage(ctx context.Context, rt Runtime, now time.Time) map[int64]float64 {
	lookback := 30
	for _, rule := range rt.ActiveRules {
		if rule.Enabled && (rule.Type == RuleUsedWithinDays || rule.Type == RuleActiveWithinDays) && rule.Days > lookback {
			lookback = rule.Days
		}
	}
	if lookback > 365 {
		lookback = 365
	}
	from := now.AddDate(0, 0, -(lookback - 1)).Format("2006-01-02")
	to := now.Format("2006-01-02")
	res, err := s.client.FetchUsageRanking(ctx, "", sub2api.ClientMeta{}, sub2api.UsageRankQuery{FromDate: from, ToDate: to, Limit: rt.MaxUsers})
	if err != nil || res == nil {
		return map[int64]float64{}
	}
	out := make(map[int64]float64, len(res.Items))
	for _, item := range res.Items {
		out[item.UserID] = item.Amount
	}
	return out
}

func (s *Service) recordError(err error) {
	s.mu.Lock()
	s.failed++
	s.lastError = err.Error()
	s.mu.Unlock()
}

func (s *Service) recordExecutionFailure(batchID int64, err error) {
	s.mu.Lock()
	s.failed++
	s.lastBatchID = batchID
	s.lastStatus = store.RedistributionBatchFailed
	if err != nil {
		s.lastError = err.Error()
	}
	s.mu.Unlock()
}

func (s *Service) publishFinished(batch store.RedistributionBatch, donorFailures, rewardFailures int) {
	if s.notifier == nil {
		return
	}
	level := notify.LevelInfo
	if donorFailures > 0 || rewardFailures > 0 || batch.Status == store.RedistributionBatchPartial {
		level = notify.LevelWarn
	}
	s.notifier.Publish(notify.Event{
		Type: notify.TypeRedistributionFinished, Level: level, Title: "额度回流批次完成",
		Text: fmt.Sprintf("批次 #%d 回收 %.4f，发放 %.4f", batch.ID, batch.ActualReclaim, batch.ActualDistribute),
		Fields: []notify.Field{
			{Key: "状态", Value: batch.Status},
			{Key: "回收用户", Value: fmt.Sprintf("%d", batch.CandidateCount)},
			{Key: "奖励用户", Value: fmt.Sprintf("%d", batch.RecipientCount)},
			{Key: "回收失败", Value: fmt.Sprintf("%d", donorFailures)},
			{Key: "发放失败", Value: fmt.Sprintf("%d", rewardFailures)},
		},
	})
}

func splitEntries(entries []store.RedistributionEntry) ([]store.RedistributionEntry, []store.RedistributionEntry) {
	donors := []store.RedistributionEntry{}
	rewards := []store.RedistributionEntry{}
	for _, entry := range entries {
		if entry.Role == store.RedistributionRoleDonor {
			donors = append(donors, entry)
		} else if entry.Role == store.RedistributionRoleRecipient {
			rewards = append(rewards, entry)
		}
	}
	return donors, rewards
}

func displayName(user sub2api.User) string {
	if strings.TrimSpace(user.Username) != "" {
		return strings.TrimSpace(user.Username)
	}
	if strings.TrimSpace(user.Email) != "" {
		return strings.TrimSpace(user.Email)
	}
	return fmt.Sprintf("用户 #%d", user.ID)
}

func activitySortTime(snap UserSnapshot) time.Time {
	values := []*time.Time{snap.User.LastUsedAt, snap.User.LastActiveAt, snap.ExtensionAt}
	var latest time.Time
	for _, value := range values {
		if value != nil && value.After(latest) {
			latest = *value
		}
	}
	if latest.IsZero() {
		return snap.User.CreatedAt
	}
	return latest
}

func failureExceeded(rt Runtime, failures, total int) bool {
	if failures <= 0 || total <= 0 || rt.FailureThresholdPercent <= 0 {
		return false
	}
	return float64(failures)*100/float64(total) >= rt.FailureThresholdPercent
}

func stopped(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func nextDue(rt Runtime, now time.Time) time.Time {
	loc, err := time.LoadLocation(rt.Timezone)
	if err != nil {
		return time.Time{}
	}
	expr, err := patrol.ParseCron(rt.Cron)
	if err != nil {
		return time.Time{}
	}
	return expr.Next(now.In(loc))
}
