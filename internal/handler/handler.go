package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"sub2api-ext/internal/config"
	"sub2api-ext/internal/lottery"
	"sub2api-ext/internal/metrics"
	"sub2api-ext/internal/modules"
	"sub2api-ext/internal/notify"
	"sub2api-ext/internal/ratelimit"
	"sub2api-ext/internal/report"
	"sub2api-ext/internal/settings"
	"sub2api-ext/internal/patrol"
	"sub2api-ext/internal/store"
	"sub2api-ext/internal/sub2api"
)

const highAmountThreshold = 10.0
const minRewardUnit = 0.0001

type Handler struct {
	cfg      config.Config
	store    *store.Store
	client   *sub2api.Client
	settings *settings.Service
	patrol   *patrol.Service
	notifier *notify.Notifier
	lottery  *lottery.Settings
	report   *report.Service

	limitCheckin    *ratelimit.Limiter
	limitStatus     *ratelimit.Limiter
	limitAdminWrite *ratelimit.Limiter
	limitAdminRead  *ratelimit.Limiter
}

func New(cfg config.Config, st *store.Store, client *sub2api.Client, stg *settings.Service, patrolSvc *patrol.Service) *Handler {
	sec := cfg.Security
	h := &Handler{
		cfg:             cfg,
		store:           st,
		client:          client,
		settings:        stg,
		patrol:          patrolSvc,
		limitCheckin:    ratelimit.New(sec.RateCheckinPerMin, time.Minute),
		limitStatus:     ratelimit.New(sec.RateStatusPerMin, time.Minute),
		limitAdminWrite: ratelimit.New(sec.RateAdminWritePerMin, time.Minute),
		limitAdminRead:  ratelimit.New(sec.RateAdminReadPerMin, time.Minute),
	}
	h.syncAdminCred()
	return h
}

// SetNotifier attaches an optional notifier. Safe to leave nil.
func (h *Handler) SetNotifier(n *notify.Notifier) { h.notifier = n }

// SetReport attaches the daily report service. Safe to leave nil.
func (h *Handler) SetReport(s *report.Service) { h.report = s }

func (h *Handler) publish(ev notify.Event) {
	if h.notifier == nil {
		return
	}
	h.notifier.Publish(ev)
}

type statusResponse struct {
	Enabled         bool         `json:"enabled"`
	RewardMode      string       `json:"reward_mode"`
	RewardAmount    float64      `json:"reward_amount"`
	RewardMin       float64      `json:"reward_min"`
	RewardMax       float64      `json:"reward_max"`
	RandomReward    bool         `json:"random_reward"`
	Timezone        string       `json:"timezone"`
	Today           string       `json:"today"`
	HardCap         float64      `json:"hard_cap"`
	DailyBudget     float64      `json:"daily_budget"`
	DailySpent      float64      `json:"daily_spent"`
	DailyRemaining  *float64     `json:"daily_remaining,omitempty"`
	BudgetAction    string       `json:"budget_action"`
	Clamped         bool         `json:"clamped,omitempty"`
	CheckedInToday  bool         `json:"checked_in_today"`
	TodayReward     *float64     `json:"today_reward,omitempty"`
	TotalCheckins   int64        `json:"total_checkins"`
	UserID          int64        `json:"user_id,omitempty"`
	Email           string       `json:"email,omitempty"`
	Username        string       `json:"username,omitempty"`
	Balance         *float64     `json:"balance,omitempty"`
	IsAdmin         bool         `json:"is_admin,omitempty"`
	Recent          []recentItem `json:"recent,omitempty"`
	StreakEnabled   bool         `json:"streak_enabled"`
	StreakStep      float64      `json:"streak_step"`
	StreakMaxDays   int          `json:"streak_max_days"`
	CurrentStreak   int          `json:"current_streak"`
	NextStreak      int          `json:"next_streak"`
	StreakBonus     float64      `json:"streak_bonus"`
	MilestoneBonus  float64      `json:"milestone_bonus"`
	StreakMilestones map[int]float64 `json:"streak_milestones,omitempty"`
	NextMilestoneDay int         `json:"next_milestone_day,omitempty"`
	NextMilestoneAmt float64     `json:"next_milestone_amount,omitempty"`
}

type recentItem struct {
	Date       string  `json:"date"`
	Amount     float64 `json:"amount"`
	NewBalance float64 `json:"new_balance"`
}

type checkinResponse struct {
	Message        string  `json:"message"`
	Status         string  `json:"status"`
	Amount         float64 `json:"amount,omitempty"`
	NewBalance     float64 `json:"new_balance,omitempty"`
	CheckinDate    string  `json:"checkin_date,omitempty"`
	CheckedInToday bool    `json:"checked_in_today"`
}

type adminUpdateBody struct {
	settings.UpdateInput
	Source            string  `json:"source"`
	ConfirmHighAmount *bool   `json:"confirm_high_amount"`
	// AdminAPIKey: non-empty sets SQLite override (takes precedence over env).
	// Empty / omitted: no change. Use AdminAPIKeyClear to remove override.
	AdminAPIKey      *string `json:"admin_api_key"`
	AdminAPIKeyClear *bool   `json:"admin_api_key_clear"`
}

func (h *Handler) Health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"product":      modules.ProductID,
		"product_name": modules.ProductName,
		"compat_name":  modules.CompatName,
		"modules":      modules.ActiveIDs(),
		"metrics":      metrics.Get(),
	})
}

func (h *Handler) Ready(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := map[string]any{"ok": true}
	if err := h.store.PingDB(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "sqlite": err.Error()})
		return
	}
	out["sqlite"] = "ok"
	if err := h.client.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "sqlite": "ok", "sub2api": err.Error()})
		return
	}
	out["sub2api"] = "ok"
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) Metrics(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.Header.Get("Accept"), "text/plain") || r.URL.Query().Get("format") == "prom" {
		metrics.WritePrometheus(w)
		return
	}
	metrics.WriteJSON(w)
}

func (h *Handler) Status(w http.ResponseWriter, r *http.Request) {
	if !h.limitStatus.Allow("status:"+clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	rt := h.settings.Get()
	today := h.settings.Today()
	spent, _ := h.store.SumAmountByDate(r.Context(), today)
	resp := statusResponse{
		Enabled:      rt.Enabled,
		RewardMode:   rt.RewardMode,
		RewardAmount: rt.RewardAmount,
		RewardMin:    rt.RewardMin,
		RewardMax:    rt.RewardMax,
		RandomReward: rt.RandomReward || rt.RewardMode == "random",
		Timezone:     rt.Timezone,
		Today:        today,
		HardCap:      rt.HardCap,
		DailyBudget:  rt.DailyBudget,
		DailySpent:   spent,
		BudgetAction: rt.BudgetAction,
		Clamped:       rt.Clamped,
		StreakEnabled:    rt.StreakEnabled,
		StreakStep:       rt.StreakStep,
		StreakMaxDays:    rt.StreakMaxDays,
		StreakMilestones: rt.StreakMilestones,
	}
	if rt.DailyBudget > 0 {
		rem := rt.DailyBudget - spent
		if rem < 0 {
			rem = 0
		}
		resp.DailyRemaining = &rem
	}

	token := extractToken(r)
	if token == "" {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	user, err := h.client.ResolveUser(r.Context(), token, clientMetaFromRequest(r))
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "invalid token: "+err.Error())
		return
	}
	resp.UserID = user.ID
	resp.Email = user.Email
	resp.Username = user.Username
	bal := user.Balance
	resp.Balance = &bal
	resp.IsAdmin = strings.EqualFold(user.Role, "admin")

	existing, err := h.store.Get(r.Context(), user.ID, today)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if existing != nil {
		resp.CheckedInToday = true
		amt := existing.Amount
		resp.TodayReward = &amt
	}

	total, err := h.store.CountByUser(r.Context(), user.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp.TotalCheckins = total

	before, _ := h.store.CountStreakBefore(r.Context(), user.ID, today)
	streakForBonus := before + 1
	if existing != nil {
		resp.CurrentStreak = before + 1
		resp.NextStreak = before + 1
	} else {
		resp.CurrentStreak = before
		resp.NextStreak = before + 1
	}
	stepB, mileB, totalB := settings.TotalStreakBonus(rt, streakForBonus)
	resp.StreakBonus = totalB
	resp.MilestoneBonus = mileB
	_ = stepB
	resp.StreakMilestones = rt.StreakMilestones
	nd, na := nextMilestone(rt, streakForBonus)
	resp.NextMilestoneDay = nd
	resp.NextMilestoneAmt = na

	records, err := h.store.ListRecent(r.Context(), user.ID, 7)
	if err == nil {
		for _, rec := range records {
			resp.Recent = append(resp.Recent, recentItem{
				Date:       rec.CheckinDate,
				Amount:     rec.Amount,
				NewBalance: rec.NewBalance,
			})
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) Checkin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	tok := extractToken(r)
	key := "checkin:" + clientIP(r)
	if tok != "" {
		if len(tok) > 16 {
			key += ":" + tok[:16]
		} else {
			key += ":" + tok
		}
	}
	if !h.limitCheckin.Allow(key) {
		writeErr(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	rt := h.settings.Get()
	if !rt.Enabled {
		metrics.CheckinDisabled.Add(1)
		writeJSON(w, http.StatusOK, checkinResponse{
			Message: "check-in is disabled",
			Status:  "disabled",
		})
		return
	}
	if h.effectiveAdminCred() == "" {
		writeErr(w, http.StatusServiceUnavailable, "server missing SUB2API_ADMIN_API_KEY (configure in admin UI or env)")
		return
	}

	token := extractToken(r)
	if token == "" {
		writeErr(w, http.StatusUnauthorized, "missing token")
		return
	}

	user, err := h.client.ResolveUser(r.Context(), token, clientMetaFromRequest(r))
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "invalid token: "+err.Error())
		return
	}

	today := h.settings.Today()
	if existing, err := h.store.Get(r.Context(), user.ID, today); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	} else if existing != nil {
		writeAlready(w, existing)
		return
	}

	reward := h.settings.PickReward()
	before, _ := h.store.CountStreakBefore(r.Context(), user.ID, today)
	streakToday := before + 1
	_, _, extra := settings.TotalStreakBonus(rt, streakToday)
	if extra > 0 {
		reward = reward + extra
		if rt.HardCap > 0 && reward > rt.HardCap {
			reward = rt.HardCap
		}
		reward = float64(int(reward*10000+0.5)) / 10000
	}
	spent, err := h.store.SumAmountByDate(r.Context(), today)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	reward, budgetStatus, msg := applyDailyBudget(reward, spent, rt)
	if budgetStatus != "" {
		if budgetStatus == "budget_exhausted" {
			metrics.CheckinBudget.Add(1)
			h.publish(notify.Event{
				Type:  notify.TypeCheckinBudget,
				Level: notify.LevelWarn,
				Title: "签到日预算耗尽",
				Text:  "今日签到预算已用完，后续签到将被拒绝",
				Fields: []notify.Field{
					{Key: "日期", Value: today},
					{Key: "日预算", Value: strconv.FormatFloat(rt.DailyBudget, 'f', 4, 64)},
					{Key: "已发放", Value: strconv.FormatFloat(spent, 'f', 4, 64)},
					{Key: "预算动作", Value: rt.BudgetAction},
				},
			})
		}
		if budgetStatus == "budget_exhausted" && rt.BudgetAction == settings.BudgetDisable {
			disabled := false
			_, _ = h.settings.Update(r.Context(), settings.UpdateInput{Enabled: &disabled})
		}
		writeJSON(w, http.StatusOK, checkinResponse{
			Message: msg,
			Status:  budgetStatus,
		})
		return
	}

	estimated := user.Balance + reward
	rec, err := h.store.TryInsert(r.Context(), user.ID, today, reward, estimated)
	if err != nil {
		if errors.Is(err, store.ErrAlreadyCheckedIn) {
			if ex, _ := h.store.Get(r.Context(), user.ID, today); ex != nil {
				writeAlready(w, ex)
				return
			}
			writeJSON(w, http.StatusOK, checkinResponse{
				Message:        "already checked in today",
				Status:         "already_checked_in",
				CheckedInToday: true,
				CheckinDate:    today,
			})
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	notes := fmt.Sprintf("%s %s +%.4f", rt.NotesPrefix, today, reward)
	updated, err := h.creditWithRetry(r.Context(), user.ID, reward, notes, today)
	if err != nil {
		// 明确失败才删占坑；幂等命中已在 creditWithRetry 内视为成功
		_ = h.store.Delete(r.Context(), rec.ID)
		metrics.CheckinFail.Add(1)
		writeErr(w, http.StatusBadGateway, "credit balance failed: "+err.Error())
		return
	}

	finalBal := updated.Balance
	if finalBal == 0 {
		finalBal = estimated
	}
	_ = h.store.UpdateNewBalance(r.Context(), rec.ID, finalBal)

	metrics.CheckinSuccess.Add(1)
	writeJSON(w, http.StatusOK, checkinResponse{
		Message:        "check-in success",
		Status:         "success",
		Amount:         reward,
		NewBalance:     finalBal,
		CheckinDate:    today,
		CheckedInToday: true,
	})
}

// creditWithRetry: one retry on transient error; idempotent hit keeps success.
func (h *Handler) creditWithRetry(ctx context.Context, userID int64, amount float64, notes, date string) (*sub2api.User, error) {
	u, err := h.client.AddBalance(ctx, userID, amount, notes, date)
	if err == nil {
		return u, nil
	}
	// retry once
	metrics.CreditRetry.Add(1)
	u2, err2 := h.client.AddBalance(ctx, userID, amount, notes, date)
	if err2 == nil {
		metrics.CreditIdempotent.Add(1)
		return u2, nil
	}
	// if either error looks idempotent and we can load user, treat ok
	if u3, e3 := h.client.GetUserByAdmin(ctx, userID); e3 == nil {
		// only accept if second error mentions idempotency-ish, else still fail
		msg := strings.ToLower(err2.Error() + " " + err.Error())
		if strings.Contains(msg, "idempoten") || strings.Contains(msg, "duplicate") || strings.Contains(msg, "already") || strings.Contains(msg, "409") {
			metrics.CreditIdempotent.Add(1)
			return u3, nil
		}
	}
	return nil, err2
}

// applyDailyBudget returns adjusted reward; if blocked, status is non-empty.
func applyDailyBudget(reward, spent float64, rt settings.Runtime) (float64, string, string) {
	if rt.HardCap > 0 && reward > rt.HardCap {
		reward = rt.HardCap
	}
	if rt.DailyBudget <= 0 {
		return reward, "", ""
	}
	remain := rt.DailyBudget - spent
	if remain < minRewardUnit {
		return 0, "budget_exhausted", "daily budget exhausted"
	}
	if reward > remain {
		reward = remain
	}
	if reward < minRewardUnit {
		return 0, "budget_exhausted", "daily budget exhausted"
	}
	// round to money unit
	reward = float64(int(reward*10000+0.5)) / 10000
	if reward < minRewardUnit {
		return 0, "budget_exhausted", "daily budget exhausted"
	}
	return reward, "", ""
}

func (h *Handler) AdminGetSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.limitAdminRead.Allow("ar:"+clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	if _, err := h.requireAdmin(r); err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	rt := h.settings.Get()
	writeJSON(w, http.StatusOK, h.settingsPayload(rt, h.settings.Today()))
}

func (h *Handler) AdminListAudit(w http.ResponseWriter, r *http.Request) {
	if !h.limitAdminRead.Allow("AdminListAudit:"+clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, err := h.requireAdmin(r); err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	limit := 10
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	list, err := h.store.ListSettingsAudit(r.Context(), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]map[string]any, 0, len(list))
	for _, a := range list {
		items = append(items, auditPayload(a))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (h *Handler) AdminUpdateSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.limitAdminWrite.Allow("aw:"+clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	actor, err := h.requireAdmin(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read body failed")
		return
	}
	var in adminUpdateBody
	if err := json.Unmarshal(body, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}

	clearKey := in.AdminAPIKeyClear != nil && *in.AdminAPIKeyClear
	setKey := in.AdminAPIKey != nil && strings.TrimSpace(*in.AdminAPIKey) != ""
	hasKeyOp := clearKey || setKey
	hasSettings := hasSettingsFields(in.UpdateInput)
	if !hasKeyOp && !hasSettings {
		writeErr(w, http.StatusBadRequest, "no fields to update")
		return
	}

	oldRT := h.settings.Get()
	if hasSettings {
		if err := h.guardSensitiveWrite(r, in.UpdateInput, oldRT); err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
		if needsHighAmountConfirm(in.UpdateInput) {
			confirmed := in.ConfirmHighAmount != nil && *in.ConfirmHighAmount
			if !confirmed {
				writeErr(w, http.StatusBadRequest, fmt.Sprintf(
					"reward amount exceeds %.0f; set confirm_high_amount=true to proceed", highAmountThreshold))
				return
			}
		}
	}

	oldJSON, _ := json.Marshal(h.settingsPayload(oldRT, h.settings.Today()))

	source := strings.TrimSpace(strings.ToLower(in.Source))
	if source == "" {
		source = "api"
	}
	if source != "ui" && source != "api" && source != "rollback" {
		source = "api"
	}

	if hasKeyOp {
		if clearKey {
			if err := h.settings.ClearAdminAPIKey(r.Context()); err != nil {
				writeErr(w, http.StatusInternalServerError, "clear admin api key: "+err.Error())
				return
			}
		} else if setKey {
			if err := h.settings.SetAdminAPIKey(r.Context(), strings.TrimSpace(*in.AdminAPIKey)); err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		h.syncAdminCred()
	}

	rt := h.settings.Get()
	if hasSettings {
		var err error
		rt, err = h.settings.Update(r.Context(), in.UpdateInput)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	newJSON, _ := json.Marshal(h.settingsPayload(rt, h.settings.Today()))
	h.writeAudit(r, actor, source, string(oldJSON), string(newJSON))

	out := h.settingsPayload(rt, h.settings.Today())
	if hasKeyOp && !hasSettings {
		out["message"] = "admin api key updated"
	} else if hasKeyOp {
		out["message"] = "settings and admin api key updated"
	} else {
		out["message"] = "settings updated"
	}
	if list, err := h.store.ListSettingsAudit(r.Context(), 10); err == nil {
		items := make([]map[string]any, 0, len(list))
		for _, a := range list {
			items = append(items, auditPayload(a))
		}
		out["audit"] = items
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) AdminRollbackSettings(w http.ResponseWriter, r *http.Request) {
	if !h.limitAdminWrite.Allow("AdminRollbackSettings:"+clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	actor, err := h.requireAdmin(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read body failed")
		return
	}
	var req struct {
		AuditID int64 `json:"audit_id"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.AuditID <= 0 {
		writeErr(w, http.StatusBadRequest, "audit_id required")
		return
	}
	a, err := h.store.GetSettingsAudit(r.Context(), req.AuditID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if a == nil {
		writeErr(w, http.StatusNotFound, "audit not found")
		return
	}
	var oldMap map[string]any
	if err := json.Unmarshal([]byte(a.OldJSON), &oldMap); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid audit old_json")
		return
	}
	in := settings.ApplyMapToUpdateInput(oldMap)
	// ensure full apply of reward fields for rollback
	if in.Enabled == nil && in.RewardMode == nil && in.RewardAmount == nil &&
		in.RewardMin == nil && in.RewardMax == nil && in.Timezone == nil &&
		in.NotesPrefix == nil && in.HardCap == nil && in.DailyBudget == nil && in.BudgetAction == nil {
		writeErr(w, http.StatusBadRequest, "audit old_json has no settings fields")
		return
	}

	oldRT := h.settings.Get()
	oldJSON, _ := json.Marshal(h.settingsPayload(oldRT, h.settings.Today()))
	rt, err := h.settings.Update(r.Context(), in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "rollback failed: "+err.Error())
		return
	}
	newJSON, _ := json.Marshal(h.settingsPayload(rt, h.settings.Today()))
	h.writeAudit(r, actor, "rollback", string(oldJSON), string(newJSON))
	out := h.settingsPayload(rt, h.settings.Today())
	out["message"] = "settings rolled back"
	out["rolled_back_from_audit_id"] = req.AuditID
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) AdminStats(w http.ResponseWriter, r *http.Request) {
	if !h.limitAdminRead.Allow("AdminStats:"+clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, err := h.requireAdmin(r); err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	rt := h.settings.Get()
	today := h.settings.Today()
	date := strings.TrimSpace(r.URL.Query().Get("date"))
	if date == "" {
		date = today
	}
	st, err := h.store.StatsByDate(r.Context(), date)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	avg := 0.0
	if st.Count > 0 {
		avg = st.TotalAmount / float64(st.Count)
	}
	var remaining *float64
	if rt.DailyBudget > 0 {
		rem := rt.DailyBudget - st.TotalAmount
		if rem < 0 {
			rem = 0
		}
		remaining = &rem
	}

	// last 7 days bars relative to today (config timezone)
	loc := h.settings.Location()
	end, err := time.ParseInLocation("2006-01-02", today, loc)
	if err != nil {
		end = time.Now().In(loc)
	}
	from := end.AddDate(0, 0, -6).Format("2006-01-02")
	to := end.Format("2006-01-02")
	days, _ := h.store.StatsRecentDays(r.Context(), from, to)
	dayItems := make([]map[string]any, 0, len(days))
	for _, d := range days {
		dayItems = append(dayItems, map[string]any{
			"date":         d.Date,
			"count":        d.Count,
			"total_amount": d.TotalAmount,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"date":            date,
		"today":           today,
		"checkin_count":   st.Count,
		"total_amount":    st.TotalAmount,
		"avg_amount":      avg,
		"hard_cap":        rt.HardCap,
		"daily_budget":    rt.DailyBudget,
		"daily_remaining": remaining,
		"budget_action":   rt.BudgetAction,
		"recent_days":     dayItems,
	})
}

func (h *Handler) AdminListCheckins(w http.ResponseWriter, r *http.Request) {
	if !h.limitAdminRead.Allow("AdminListCheckins:"+clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, err := h.requireAdmin(r); err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	date := strings.TrimSpace(r.URL.Query().Get("date"))
	if date == "" {
		date = h.settings.Today()
	}
	limit := 50
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	list, err := h.store.ListByDate(r.Context(), date, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]map[string]any, 0, len(list))
	for _, rec := range list {
		items = append(items, map[string]any{
			"id":           rec.ID,
			"user_id":      rec.UserID,
			"checkin_date": rec.CheckinDate,
			"amount":       rec.Amount,
			"new_balance":  rec.NewBalance,
			"created_at":   rec.CreatedAt.UTC().Format(time.RFC3339),
			"idempotency_key": fmt.Sprintf("checkin-%d-%s", rec.UserID, rec.CheckinDate),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"date":  date,
		"items": items,
		"count": len(items),
	})
}

func needsHighAmountConfirm(in settings.UpdateInput) bool {
	if in.RewardAmount != nil && *in.RewardAmount > highAmountThreshold {
		return true
	}
	if in.RewardMax != nil && *in.RewardMax > highAmountThreshold {
		return true
	}
	if in.RewardMin != nil && *in.RewardMin > highAmountThreshold {
		return true
	}
	if in.HardCap != nil && *in.HardCap > highAmountThreshold {
		return true
	}
	return false
}

func classifyActor(user *sub2api.User) (actorType, actorName string) {
	if user == nil {
		return "server_admin", "server-admin"
	}
	name := strings.TrimSpace(user.Username)
	if name == "" {
		name = strings.TrimSpace(user.Email)
	}
	if user.ID == 0 || strings.EqualFold(user.Username, "server-admin") {
		if name == "" {
			name = "server-admin"
		}
		return "server_admin", name
	}
	if name == "" {
		name = fmt.Sprintf("admin-%d", user.ID)
	}
	return "user_admin", name
}

func auditPayload(a store.SettingsAudit) map[string]any {
	return map[string]any{
		"id":         a.ID,
		"actor_type": a.ActorType,
		"actor_id":   a.ActorID,
		"actor_name": a.ActorName,
		"source":     a.Source,
		"old_json":   a.OldJSON,
		"new_json":   a.NewJSON,
		"client_ip":  a.ClientIP,
		"user_agent": a.UserAgent,
		"created_at": a.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func (h *Handler) writeAudit(r *http.Request, actor *sub2api.User, source, oldJSON, newJSON string) {
	actorType, actorName := classifyActor(actor)
	h.publish(notify.Event{
		Type:  notify.TypeSettingsChanged,
		Level: notify.LevelWarn,
		Title: "扩展配置被修改",
		Text:  "签到配置发生变更",
		Fields: []notify.Field{
			{Key: "操作者", Value: actorName + " (" + actorType + ")"},
			{Key: "来源", Value: source},
			{Key: "来源 IP", Value: clientIP(r)},
			{Key: "变更后", Value: truncateForNotify(newJSON, 300)},
		},
	})
	_, _ = h.store.InsertSettingsAudit(r.Context(), store.SettingsAudit{
		ActorType: actorType,
		ActorID:   actor.ID,
		ActorName: actorName,
		Source:    source,
		OldJSON:   oldJSON,
		NewJSON:   newJSON,
		ClientIP:  clientIP(r),
		UserAgent: r.Header.Get("User-Agent"),
		CreatedAt: time.Now().UTC(),
	})
}

func (h *Handler) guardSensitiveWrite(r *http.Request, in settings.UpdateInput, old settings.Runtime) error {
	if !h.cfg.Security.SensitiveWriteRequireAPIKey {
		return nil
	}
	if !isSensitiveUpdate(in, old) {
		return nil
	}
	if h.isServerAdminCredential(r) {
		return nil
	}
	return fmt.Errorf("敏感配置升高需要服务端 Admin API Key（请求头 x-api-key）。请在「鉴权与密钥」粘贴与服务器一致的 Key 后点「使用」，再保存")
}

// isSensitiveUpdate only flags riskier *increases* (not re-saving the same high values).
// daily_budget=0 is never treated as sensitive.
func isSensitiveUpdate(in settings.UpdateInput, old settings.Runtime) bool {
	if in.HardCap != nil && *in.HardCap > highAmountThreshold && *in.HardCap > old.HardCap {
		return true
	}
	if in.RewardMax != nil && *in.RewardMax > highAmountThreshold && *in.RewardMax > old.RewardMax {
		return true
	}
	if in.RewardAmount != nil && *in.RewardAmount > highAmountThreshold && *in.RewardAmount > old.RewardAmount {
		return true
	}
	if in.RewardMin != nil && *in.RewardMin > highAmountThreshold && *in.RewardMin > old.RewardMin {
		return true
	}
	if in.DailyBudget != nil && *in.DailyBudget > 0 && *in.DailyBudget > old.DailyBudget {
		return true
	}
	if in.StreakMilestones != nil {
		for day, amt := range in.StreakMilestones {
			if amt <= highAmountThreshold {
				continue
			}
			oldAmt := 0.0
			if old.StreakMilestones != nil {
				oldAmt = old.StreakMilestones[day]
			}
			if amt > oldAmt {
				return true
			}
		}
	}
	return false
}

func nextMilestone(rt settings.Runtime, streak int) (day int, amount float64) {
	if !rt.StreakEnabled || len(rt.StreakMilestones) == 0 {
		return 0, 0
	}
	best := 0
	for d := range rt.StreakMilestones {
		if d > streak && (best == 0 || d < best) {
			best = d
		}
	}
	if best == 0 {
		return 0, 0
	}
	return best, rt.StreakMilestones[best]
}

// effectiveAdminCred: SQLite UI override > env SUB2API_ADMIN_API_KEY/TOKEN.
func (h *Handler) effectiveAdminCred() string {
	if k := h.settings.StoredAdminAPIKey(); k != "" {
		return k
	}
	return strings.TrimSpace(h.cfg.Sub2API.AdminToken)
}

func (h *Handler) adminCredSource() string {
	if h.settings.StoredAdminAPIKey() != "" {
		return "ui"
	}
	if strings.TrimSpace(h.cfg.Sub2API.AdminToken) != "" {
		return "env"
	}
	return "none"
}

func (h *Handler) syncAdminCred() {
	h.client.SetAdminToken(h.effectiveAdminCred())
}

func (h *Handler) settingsPayload(rt settings.Runtime, today string) map[string]any {
	effective := h.effectiveAdminCred()
	return map[string]any{
		"enabled":       rt.Enabled,
		"reward_mode":   rt.RewardMode,
		"reward_amount": rt.RewardAmount,
		"reward_min":    rt.RewardMin,
		"reward_max":    rt.RewardMax,
		"random_reward": rt.RandomReward || rt.RewardMode == "random",
		"timezone":      rt.Timezone,
		"notes_prefix":  rt.NotesPrefix,
		"hard_cap":      rt.HardCap,
		"daily_budget":  rt.DailyBudget,
		"budget_action":  rt.BudgetAction,
		"clamped":        rt.Clamped,
		"streak_enabled":    rt.StreakEnabled,
		"streak_step":       rt.StreakStep,
		"streak_max_days":   rt.StreakMaxDays,
		"streak_milestones": rt.StreakMilestones,
		"today":             today,
		"admin_api_key_configured": effective != "",
		"admin_api_key_masked":     settings.MaskSecret(effective),
		"admin_api_key_source":     h.adminCredSource(),
	}
}

func hasSettingsFields(in settings.UpdateInput) bool {
	return in.Enabled != nil ||
		in.RewardMode != nil ||
		in.RewardAmount != nil ||
		in.RewardMin != nil ||
		in.RewardMax != nil ||
		in.Timezone != nil ||
		in.NotesPrefix != nil ||
		in.HardCap != nil ||
		in.DailyBudget != nil ||
		in.BudgetAction != nil ||
		in.StreakEnabled != nil ||
		in.StreakStep != nil ||
		in.StreakMaxDays != nil ||
		in.StreakMilestones != nil
}


func (h *Handler) requireAdmin(r *http.Request) (*sub2api.User, error) {
	if h.isServerAdminCredential(r) {
		return &sub2api.User{ID: 0, Role: "admin", Username: "server-admin"}, nil
	}
	token := extractToken(r)
	if token == "" {
		metrics.AuthFail.Add(1)
		return nil, fmt.Errorf("admin auth required (login as admin or pass x-api-key)")
	}
	user, err := h.client.ResolveUser(r.Context(), token, clientMetaFromRequest(r))
	if err != nil {
		metrics.AuthFail.Add(1)
		return nil, fmt.Errorf("invalid token: %w", err)
	}
	if !strings.EqualFold(user.Role, "admin") {
		metrics.AuthFail.Add(1)
		return nil, fmt.Errorf("admin role required")
	}
	return user, nil
}

func (h *Handler) isServerAdminCredential(r *http.Request) bool {
	cred := h.effectiveAdminCred()
	if cred == "" {
		return false
	}
	if k := strings.TrimSpace(r.Header.Get("x-api-key")); k != "" && subtleEqual(k, cred) {
		return true
	}
	if auth := r.Header.Get("Authorization"); auth != "" {
		parts := strings.SplitN(auth, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			if subtleEqual(strings.TrimSpace(parts[1]), cred) {
				return true
			}
		}
	}
	return false
}

func subtleEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

func writeAlready(w http.ResponseWriter, existing *store.Record) {
	metrics.CheckinAlready.Add(1)
	writeJSON(w, http.StatusOK, checkinResponse{
		Message:        "already checked in today",
		Status:         "already_checked_in",
		Amount:         existing.Amount,
		NewBalance:     existing.NewBalance,
		CheckinDate:    existing.CheckinDate,
		CheckedInToday: true,
	})
}

func clientMetaFromRequest(r *http.Request) sub2api.ClientMeta {
	return sub2api.ClientMeta{
		ClientIP:     clientIP(r),
		UserAgent:    r.Header.Get("User-Agent"),
		AcceptLang:   r.Header.Get("Accept-Language"),
		ForwardedFor: r.Header.Get("X-Forwarded-For"),
		XRealIP:      r.Header.Get("X-Real-IP"),
		CFConnecting: r.Header.Get("CF-Connecting-IP"),
	}
}

func clientIP(r *http.Request) string {
	for _, h := range []string{"CF-Connecting-IP", "True-Client-IP", "X-Real-IP"} {
		if v := strings.TrimSpace(r.Header.Get(h)); v != "" {
			return firstIP(v)
		}
	}
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		return firstIP(v)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func firstIP(v string) string {
	v = strings.TrimSpace(v)
	if i := strings.Index(v, ","); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	return v
}

func extractToken(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); auth != "" {
		parts := strings.SplitN(auth, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			return cleanToken(parts[1])
		}
		return cleanToken(auth)
	}
	if t := r.Header.Get("X-User-Token"); t != "" {
		return cleanToken(t)
	}
	if t := r.URL.Query().Get("token"); t != "" {
		return cleanToken(t)
	}
	return ""
}

func cleanToken(t string) string {
	t = strings.TrimSpace(t)
	if t == "" {
		return ""
	}
	if len(t) > 7 && strings.EqualFold(t[:7], "Bearer ") {
		t = strings.TrimSpace(t[7:])
	}
	t = strings.Trim(t, `"'`)
	if strings.Contains(t, " ") && strings.Count(t, ".") >= 2 {
		t = strings.ReplaceAll(t, " ", "+")
	}
	return strings.TrimSpace(t)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{
		"error":  msg,
		"status": status,
	})
}


func (h *Handler) AdminApplyTemplate(w http.ResponseWriter, r *http.Request) {
	if !h.limitAdminWrite.Allow("AdminApplyTemplate:"+clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	actor, err := h.requireAdmin(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read body failed")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeErr(w, http.StatusBadRequest, "name required: daily|promo|off")
		return
	}
	in, err := settings.ApplyTemplate(req.Name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	oldRT := h.settings.Get()
	if err := h.guardSensitiveWrite(r, in, oldRT); err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	oldJSON, _ := json.Marshal(h.settingsPayload(oldRT, h.settings.Today()))
	rt, err := h.settings.Update(r.Context(), in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	newJSON, _ := json.Marshal(h.settingsPayload(rt, h.settings.Today()))
	h.writeAudit(r, actor, "template:"+strings.ToLower(strings.TrimSpace(req.Name)), string(oldJSON), string(newJSON))
	out := h.settingsPayload(rt, h.settings.Today())
	out["message"] = "template applied"
	out["template"] = req.Name
	writeJSON(w, http.StatusOK, out)
}


func (h *Handler) Calendar(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.limitStatus.Allow("cal:"+clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	token := extractToken(r)
	if token == "" {
		writeErr(w, http.StatusUnauthorized, "missing token")
		return
	}
	user, err := h.client.ResolveUser(r.Context(), token, clientMetaFromRequest(r))
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "invalid token: "+err.Error())
		return
	}
	loc := h.settings.Location()
	now := time.Now().In(loc)
	year := now.Year()
	month := int(now.Month())
	if v := strings.TrimSpace(r.URL.Query().Get("year")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 2000 && n <= 2100 {
			year = n
		}
	}
	if v := strings.TrimSpace(r.URL.Query().Get("month")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 12 {
			month = n
		}
	}
	start := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, loc)
	end := start.AddDate(0, 1, -1)
	fromDate := start.Format("2006-01-02")
	toDate := end.Format("2006-01-02")
	recs, err := h.store.ListByUserMonth(r.Context(), user.ID, fromDate, toDate)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	byDate := map[string]store.Record{}
	for _, rec := range recs {
		byDate[rec.CheckinDate] = rec
	}
	days := make([]map[string]any, 0, end.Day())
	for d := 1; d <= end.Day(); d++ {
		date := time.Date(year, time.Month(month), d, 0, 0, 0, 0, loc).Format("2006-01-02")
		item := map[string]any{"date": date, "checked": false}
		if rec, ok := byDate[date]; ok {
			item["checked"] = true
			item["amount"] = rec.Amount
		}
		days = append(days, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"year":     year,
		"month":    month,
		"timezone": h.settings.Get().Timezone,
		"days":     days,
		"count":    len(recs),
	})
}


// truncateForNotify keeps notification payloads compact.
func truncateForNotify(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
