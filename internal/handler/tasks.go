package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"sub2api-ext/internal/credit"
	"sub2api-ext/internal/store"
	"sub2api-ext/internal/sub2api"
	"sub2api-ext/internal/tasks"
)

// SetTasks attaches task settings. Safe to leave nil.
func (h *Handler) SetTasks(s *tasks.Settings) { h.tasks = s }

// taskUserState is a one-shot snapshot used to render / claim tasks without N+1 queries.
type taskUserState struct {
	checkedInToday bool
	lotteryToday   bool
	streak         int
	weekCheckins   int
	weekLottery    int
	claims         map[string]store.TaskClaim
}

// TasksList GET /api/tasks
func (h *Handler) TasksList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.limitStatus.Allow("tasks-list:" + clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	if h.tasks == nil {
		writeErr(w, http.StatusServiceUnavailable, "tasks unavailable")
		return
	}
	rt := h.tasks.Get()
	token := extractToken(r)
	var user *sub2api.User
	if token != "" {
		if u, err := h.client.ResolveUser(r.Context(), token, clientMetaFromRequest(r)); err == nil {
			user = u
		}
	}
	loc := h.settings.Location()
	now := time.Now()
	today := h.settings.Today()

	var st *taskUserState
	if user != nil {
		st = h.loadTaskUserState(r.Context(), user.ID, rt.Defs, today, loc, now)
	}

	items := make([]map[string]any, 0, len(rt.Defs))
	for _, d := range rt.Defs {
		if !rt.Enabled || !d.Enabled {
			continue
		}
		target := d.Target
		if target <= 0 {
			target = 1
		}
		periodKey := tasks.PeriodKey(d, loc, now)
		progress, done := 0, false
		claimed := false
		claimAmount := 0.0
		if st != nil {
			progress, done = taskProgressFromState(d, st, target)
			if c, ok := st.claims[store.TaskClaimKey(d.ID, periodKey)]; ok {
				claimed = true
				claimAmount = c.Amount
			}
		}
		canClaim := user != nil && done && !claimed && d.Reward > 0
		items = append(items, map[string]any{
			"id": d.ID, "name": d.Name, "description": d.Description,
			"reward": d.Reward, "kind": d.Kind, "period": d.Period,
			"progress": progress, "target": target, "done": done,
			"claimed": claimed, "claim_amount": claimAmount,
			"can_claim": canClaim, "period_key": periodKey,
		})
	}
	out := map[string]any{"enabled": rt.Enabled, "items": items}
	if user != nil {
		out["user_id"] = user.ID
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) loadTaskUserState(ctx context.Context, userID int64, defs []tasks.Def, today string, loc *time.Location, now time.Time) *taskUserState {
	st := &taskUserState{claims: map[string]store.TaskClaim{}}
	needCheckin := false
	needLottery := false
	needStreak := false
	needWeekCheckin := false
	needWeekLottery := false
	periodKeys := make([]string, 0, len(defs)+1)
	periodKeys = append(periodKeys, "once")

	for _, d := range defs {
		if !d.Enabled {
			continue
		}
		periodKeys = append(periodKeys, tasks.PeriodKey(d, loc, now))
		switch d.Kind {
		case "daily_checkin":
			needCheckin = true
		case "daily_lottery":
			needLottery = true
		case "streak":
			needStreak = true
			needCheckin = true
		case "weekly_checkin":
			needWeekCheckin = true
		case "weekly_lottery":
			needWeekLottery = true
		}
	}

	if needCheckin {
		ok, _ := h.store.HasCheckedIn(ctx, userID, today)
		st.checkedInToday = ok
	}
	if needLottery {
		if dr, _ := h.store.GetLotteryDraw(ctx, userID, today); dr != nil {
			st.lotteryToday = true
		}
	}
	if needStreak {
		before, _ := h.store.CountStreakBefore(ctx, userID, today)
		st.streak = before
		if st.checkedInToday {
			st.streak = before + 1
		}
	}
	if needWeekCheckin || needWeekLottery {
		from, to := tasks.WeekRange(loc, now)
		if needWeekCheckin {
			if n, err := h.store.CountCheckinsInRange(ctx, userID, from, to); err == nil {
				st.weekCheckins = int(n)
			}
		}
		if needWeekLottery {
			if n, err := h.store.CountLotteryInRange(ctx, userID, from, to); err == nil {
				st.weekLottery = int(n)
			}
		}
	}
	if claims, err := h.store.ListTaskClaimsByPeriods(ctx, userID, periodKeys); err == nil {
		st.claims = claims
	}
	return st
}

func taskProgressFromState(d tasks.Def, st *taskUserState, target int) (progress int, done bool) {
	if st == nil {
		return 0, false
	}
	switch d.Kind {
	case "daily_checkin":
		if st.checkedInToday {
			return 1, true
		}
		return 0, false
	case "daily_lottery":
		if st.lotteryToday {
			return 1, true
		}
		return 0, false
	case "streak":
		return st.streak, st.streak >= target
	case "weekly_checkin":
		return st.weekCheckins, st.weekCheckins >= target
	case "weekly_lottery":
		return st.weekLottery, st.weekLottery >= target
	default:
		return 0, false
	}
}

// taskProgress keeps a single-task path for claim validation.
func (h *Handler) taskProgress(ctx context.Context, userID int64, d tasks.Def, today string, loc *time.Location, now time.Time) (progress int, done bool) {
	st := h.loadTaskUserState(ctx, userID, []tasks.Def{d}, today, loc, now)
	target := d.Target
	if target <= 0 {
		target = 1
	}
	return taskProgressFromState(d, st, target)
}

// TasksClaim POST /api/tasks/claim
func (h *Handler) TasksClaim(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.tasks == nil || h.credit == nil {
		writeErr(w, http.StatusServiceUnavailable, "tasks/credit unavailable")
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
	key := fmt.Sprintf("task-claim:%s:%d", clientIP(r), user.ID)
	if !h.limitCheckin.Allow(key) {
		writeErr(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	var req struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(body, &req); err != nil || strings.TrimSpace(req.TaskID) == "" {
		writeErr(w, http.StatusBadRequest, "task_id required")
		return
	}
	rt := h.tasks.Get()
	if !rt.Enabled {
		writeErr(w, http.StatusBadRequest, "tasks disabled")
		return
	}
	var def *tasks.Def
	for i := range rt.Defs {
		if rt.Defs[i].ID == req.TaskID {
			def = &rt.Defs[i]
			break
		}
	}
	if def == nil || !def.Enabled {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	if def.Reward <= 0 {
		writeErr(w, http.StatusBadRequest, "task has no claimable reward")
		return
	}
	loc := h.settings.Location()
	now := time.Now()
	today := h.settings.Today()
	_, done := h.taskProgress(r.Context(), user.ID, *def, today, loc, now)
	if !done {
		writeErr(w, http.StatusBadRequest, "task not completed")
		return
	}
	periodKey := tasks.PeriodKey(*def, loc, now)
	if c, _ := h.store.GetTaskClaim(r.Context(), user.ID, def.ID, periodKey); c != nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "already_claimed", "amount": c.Amount})
		return
	}
	// Grant first (idempotent). Claim row is written after so a crash mid-flight
	// still allows a safe retry: credit skips, claim inserts.
	h.syncAdminCred()
	res, err := h.credit.Grant(r.Context(), credit.Request{
		UserID: user.ID, Amount: def.Reward, Source: credit.SourceTask,
		SourceRef: fmt.Sprintf("task:%s:%s", def.ID, periodKey),
		Scope:     sub2api.IdempotencyScopeTask,
		Slot:      def.ID + "-" + periodKey,
		Notes:     "task:" + def.ID,
	})
	if err != nil {
		writeErr(w, http.StatusBadGateway, "credit failed: "+err.Error())
		return
	}
	ledgerID := int64(0)
	bal := 0.0
	if res != nil {
		ledgerID = res.LedgerID
		bal = res.NewBalance
		if res.User != nil {
			bal = res.User.Balance
		}
	}
	if _, err := h.store.InsertTaskClaim(r.Context(), store.TaskClaim{
		UserID: user.ID, TaskID: def.ID, PeriodKey: periodKey, Amount: def.Reward, LedgerID: ledgerID,
	}); err != nil {
		if strings.Contains(err.Error(), "already claimed") {
			writeJSON(w, http.StatusOK, map[string]any{"status": "already_claimed", "amount": def.Reward})
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "claimed", "amount": def.Reward, "new_balance": bal, "ledger_id": ledgerID,
	})
}

// AdminTasksSettings GET/PUT /api/admin/tasks/settings
func (h *Handler) AdminTasksSettings(w http.ResponseWriter, r *http.Request) {
	if _, err := h.requireAdmin(r); err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if h.tasks == nil {
		writeErr(w, http.StatusServiceUnavailable, "tasks unavailable")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, h.tasks.Get())
	case http.MethodPut, http.MethodPost:
		if !h.limitAdminWrite.Allow("AdminTasksSettings:" + clientIP(r)) {
			writeErr(w, http.StatusTooManyRequests, "rate limited")
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var rt tasks.Runtime
		if err := json.Unmarshal(body, &rt); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		if err := h.tasks.Save(r.Context(), rt); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, h.tasks.Get())
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
