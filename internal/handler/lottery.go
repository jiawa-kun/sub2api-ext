package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"sub2api-ext/internal/credit"
	"sub2api-ext/internal/lottery"
	"sub2api-ext/internal/notify"
	"sub2api-ext/internal/store"
	"sub2api-ext/internal/sub2api"
)

// SetLottery attaches the lottery settings holder. Safe to leave nil.
func (h *Handler) SetLottery(s *lottery.Settings) { h.lottery = s }

// publicPrize is the user-facing view of a prize: labels and amounts are
// shown so the pool is transparent, but weights are withheld so odds are not
// trivially scraped.
type publicPrize struct {
	Label  string  `json:"label"`
	Amount float64 `json:"amount"`
}

func publicPrizes(rt lottery.Runtime) []publicPrize {
	out := make([]publicPrize, 0, len(rt.Prizes))
	for _, p := range rt.Prizes {
		if p.Weight <= 0 {
			continue
		}
		out = append(out, publicPrize{Label: p.Label, Amount: p.Amount})
	}
	return out
}

type lotteryStatusResponse struct {
	Enabled         bool          `json:"enabled"`
	RequireCheckin  bool          `json:"require_checkin"`
	Today           string        `json:"today"`
	Prizes          []publicPrize `json:"prizes"`
	CanDraw         bool          `json:"can_draw"`
	Reason          string        `json:"reason,omitempty"`
	DrawnToday      bool          `json:"drawn_today"`
	TodayPrize      string        `json:"today_prize,omitempty"`
	TodayPrizeIndex *int          `json:"today_prize_index,omitempty"`
	TodayAmount     float64       `json:"today_amount,omitempty"`
	CheckedInToday  bool          `json:"checked_in_today"`
	BudgetLeft      *float64      `json:"budget_left,omitempty"`
}

type lotteryDrawResponse struct {
	Status     string  `json:"status"`
	Message    string  `json:"message"`
	PrizeLabel string  `json:"prize_label,omitempty"`
	PrizeIndex *int    `json:"prize_index,omitempty"`
	Amount     float64 `json:"amount,omitempty"`
	NewBalance float64 `json:"new_balance,omitempty"`
	DrawDate   string  `json:"draw_date,omitempty"`
	DrawnToday bool    `json:"drawn_today"`
}

// LotteryStatus GET /api/lottery/status
func (h *Handler) LotteryStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.limitStatus.Allow("lottery-status:" + clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	if h.lottery == nil {
		writeErr(w, http.StatusServiceUnavailable, "lottery module unavailable")
		return
	}
	rt := h.lottery.Get()
	today := h.settings.Today()
	resp := lotteryStatusResponse{
		Enabled:        rt.Enabled,
		RequireCheckin: rt.RequireCheckin,
		Today:          today,
		Prizes:         publicPrizes(rt),
	}
	if !rt.Enabled {
		resp.Reason = "抽奖未开启"
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// Anonymous callers still see the pool, just no personal state.
	token := extractToken(r)
	if token == "" {
		resp.Reason = "请先登录"
		writeJSON(w, http.StatusOK, resp)
		return
	}
	user, err := h.client.ResolveUser(r.Context(), token, clientMetaFromRequest(r))
	if err != nil {
		resp.Reason = "请先登录"
		writeJSON(w, http.StatusOK, resp)
		return
	}

	if draw, err := h.store.GetLotteryDraw(r.Context(), user.ID, today); err == nil && draw != nil {
		resp.DrawnToday = true
		resp.TodayPrize = draw.PrizeLabel
		resp.TodayPrizeIndex = publicPrizeIndex(rt, draw.PrizeIndex)
		resp.TodayAmount = draw.Amount
	}
	if rec, err := h.store.Get(r.Context(), user.ID, today); err == nil && rec != nil {
		resp.CheckedInToday = true
	}
	if rt.DailyBudget > 0 {
		spent, err := h.store.SumLotteryAmountByDate(r.Context(), today)
		if err == nil {
			left := rt.DailyBudget - spent
			if left < 0 {
				left = 0
			}
			resp.BudgetLeft = &left
		}
	}

	switch {
	case resp.DrawnToday:
		resp.Reason = "今日已抽奖"
	case rt.RequireCheckin && !resp.CheckedInToday:
		resp.Reason = "请先完成今日签到"
	case resp.BudgetLeft != nil && *resp.BudgetLeft < 0.0001:
		resp.Reason = "今日奖池已抽完"
	default:
		resp.CanDraw = true
	}
	writeJSON(w, http.StatusOK, resp)
}

// LotteryDraw POST /api/lottery/draw
func (h *Handler) LotteryDraw(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.lottery == nil {
		writeErr(w, http.StatusServiceUnavailable, "lottery module unavailable")
		return
	}
	token := extractToken(r)
	key := "lottery:" + clientIP(r)
	if token != "" {
		if len(token) > 16 {
			key += ":" + token[:16]
		} else {
			key += ":" + token
		}
	}
	if !h.limitCheckin.Allow(key) {
		writeErr(w, http.StatusTooManyRequests, "rate limited")
		return
	}

	rt := h.lottery.Get()
	if !rt.Enabled {
		writeJSON(w, http.StatusOK, lotteryDrawResponse{Status: "disabled", Message: "抽奖未开启"})
		return
	}
	if h.effectiveAdminCred() == "" {
		writeErr(w, http.StatusServiceUnavailable, "server missing SUB2API_ADMIN_API_KEY (configure in admin UI or env)")
		return
	}
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
	if rt.RequireCheckin {
		rec, err := h.store.Get(r.Context(), user.ID, today)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if rec == nil {
			writeJSON(w, http.StatusOK, lotteryDrawResponse{Status: "need_checkin", Message: "请先完成今日签到"})
			return
		}
	}

	spent, err := h.store.SumLotteryAmountByDate(r.Context(), today)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Close the entrance instead of silently turning every draw into a miss:
	// quietly changing the odds would misrepresent the advertised pool.
	if lottery.BudgetExhausted(rt, spent) {
		h.publishLotteryBudget(today, rt, spent)
		writeJSON(w, http.StatusOK, lotteryDrawResponse{Status: "budget_exhausted", Message: "今日奖池已抽完，明天再来"})
		return
	}

	// Claim the daily slot first: the UNIQUE index is the real guard against
	// concurrent double draws, so nothing is credited before it succeeds.
	drawID, err := h.store.ReserveLotteryDraw(r.Context(), user.ID, today)
	if err != nil {
		if errors.Is(err, store.ErrAlreadyDrawn) {
			existing, _ := h.store.GetLotteryDraw(r.Context(), user.ID, today)
			resp := lotteryDrawResponse{Status: "already", Message: "今日已抽奖", DrawnToday: true, DrawDate: today}
			if existing != nil {
				resp.PrizeLabel = existing.PrizeLabel
				resp.PrizeIndex = publicPrizeIndex(rt, existing.PrizeIndex)
				resp.Amount = existing.Amount
				resp.NewBalance = existing.NewBalance
			}
			writeJSON(w, http.StatusOK, resp)
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	picked := lottery.PickWithIndex(rt.Prizes, nil)
	result := lottery.Resolve(rt, picked.Prize, spent)

	if result.Status == lottery.StatusMiss {
		if err := h.store.FinalizeLotteryDrawWithIndex(r.Context(), drawID, picked.Index, picked.Prize.Label, store.PrizeTypeNone, 0, 0); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, lotteryDrawResponse{
			Status:     "miss",
			Message:    "很遗憾，未中奖",
			PrizeLabel: picked.Prize.Label,
			PrizeIndex: publicPrizeIndex(rt, picked.Index),
			DrawDate:   today,
			DrawnToday: true,
		})
		return
	}

	notes := "lottery:" + picked.Prize.Label
	newBalance := 0.0
	if h.credit != nil {
		res, err := h.credit.Grant(r.Context(), credit.Request{
			UserID: user.ID, Amount: result.Amount, Source: credit.SourceLottery,
			SourceRef: fmt.Sprintf("lottery:%d", drawID), Scope: sub2api.IdempotencyScopeLottery,
			Slot: today, Notes: notes,
		})
		if err != nil {
			_ = h.store.ReleaseLotteryDraw(r.Context(), drawID)
			writeErr(w, http.StatusBadGateway, "credit failed: "+err.Error())
			return
		}
		if res != nil {
			newBalance = res.NewBalance
			if res.User != nil {
				newBalance = res.User.Balance
			}
		}
	} else {
		credited, err := h.client.AddBalanceScoped(r.Context(), sub2api.IdempotencyScopeLottery, user.ID, result.Amount, notes, today)
		if err != nil {
			_ = h.store.ReleaseLotteryDraw(r.Context(), drawID)
			writeErr(w, http.StatusBadGateway, "credit failed: "+err.Error())
			return
		}
		if credited != nil {
			newBalance = credited.Balance
		}
	}
	if err := h.store.FinalizeLotteryDrawWithIndex(r.Context(), drawID, picked.Index, picked.Prize.Label, store.PrizeTypeBalance, result.Amount, newBalance); err != nil {
		// Balance already moved; keep the row claimed and report success.
		writeJSON(w, http.StatusOK, lotteryDrawResponse{
			Status:     "win",
			Message:    "恭喜中奖",
			PrizeLabel: picked.Prize.Label,
			PrizeIndex: publicPrizeIndex(rt, picked.Index),
			Amount:     result.Amount,
			NewBalance: newBalance,
			DrawDate:   today,
			DrawnToday: true,
		})
		return
	}
	writeJSON(w, http.StatusOK, lotteryDrawResponse{
		Status:     "win",
		Message:    "恭喜中奖",
		PrizeLabel: picked.Prize.Label,
		PrizeIndex: publicPrizeIndex(rt, picked.Index),
		Amount:     result.Amount,
		NewBalance: newBalance,
		DrawDate:   today,
		DrawnToday: true,
	})
}

func publicPrizeIndex(rt lottery.Runtime, rawIndex int) *int {
	if rawIndex < 0 {
		return nil
	}
	visibleIndex := 0
	for i, prize := range rt.Prizes {
		if prize.Weight <= 0 {
			continue
		}
		if i == rawIndex {
			return &visibleIndex
		}
		visibleIndex++
	}
	return nil
}

func (h *Handler) publishLotteryBudget(today string, rt lottery.Runtime, spent float64) {
	h.publish(notify.Event{
		Type:  notify.TypeLotteryBudget,
		Level: notify.LevelWarn,
		Title: "抽奖日预算耗尽",
		Text:  "今日抽奖预算已用完，抽奖入口已关闭",
		Fields: []notify.Field{
			{Key: "日期", Value: today},
			{Key: "日预算", Value: strconv.FormatFloat(rt.DailyBudget, 'f', 4, 64)},
			{Key: "已发放", Value: strconv.FormatFloat(spent, 'f', 4, 64)},
		},
	})
}

// AdminGetLotterySettings GET /api/admin/lottery/settings
func (h *Handler) AdminGetLotterySettings(w http.ResponseWriter, r *http.Request) {
	if !h.limitAdminRead.Allow("AdminGetLotterySettings:" + clientIP(r)) {
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
	if h.lottery == nil {
		writeErr(w, http.StatusServiceUnavailable, "lottery module unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": h.lottery.Get()})
}

// AdminUpdateLotterySettings PUT/POST /api/admin/lottery/settings
func (h *Handler) AdminUpdateLotterySettings(w http.ResponseWriter, r *http.Request) {
	if !h.limitAdminWrite.Allow("AdminUpdateLotterySettings:" + clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	actor, err := h.requireAdmin(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if h.lottery == nil {
		writeErr(w, http.StatusServiceUnavailable, "lottery module unavailable")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	var in lottery.UpdateInput
	if err := json.Unmarshal(body, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	oldRT := h.lottery.Get()
	newRT, err := h.lottery.Update(r.Context(), in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	oldJSON, _ := json.Marshal(oldRT)
	newJSON, _ := json.Marshal(newRT)
	h.writeAudit(r, actor, "lottery", string(oldJSON), string(newJSON))
	writeJSON(w, http.StatusOK, map[string]any{"settings": newRT})
}

// AdminLotteryDraws GET /api/admin/lottery/draws
func (h *Handler) AdminLotteryDraws(w http.ResponseWriter, r *http.Request) {
	if !h.limitAdminRead.Allow("AdminLotteryDraws:" + clientIP(r)) {
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
	q := r.URL.Query()
	limit, _ := strconv.Atoi(strings.TrimSpace(q.Get("limit")))
	offset, _ := strconv.Atoi(strings.TrimSpace(q.Get("offset")))
	userID, _ := strconv.ParseInt(strings.TrimSpace(q.Get("user_id")), 10, 64)
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	items, err := h.store.ListLotteryDraws(r.Context(), userID, limit, offset)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	total, err := h.store.CountLotteryDraws(r.Context(), userID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":  items,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// AdminLotteryStats GET /api/admin/lottery/stats
func (h *Handler) AdminLotteryStats(w http.ResponseWriter, r *http.Request) {
	if !h.limitAdminRead.Allow("AdminLotteryStats:" + clientIP(r)) {
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
	today := h.settings.Today()
	todayCount, err := h.store.CountLotteryDrawsByDate(r.Context(), today)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	todayAmount, err := h.store.SumLotteryAmountByDate(r.Context(), today)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	totals, err := h.store.LotteryAllTimeTotals(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	payload := map[string]any{
		"today":         today,
		"today_draws":   todayCount,
		"today_amount":  todayAmount,
		"total_draws":   totals.Draws,
		"total_winners": totals.Winners,
		"total_amount":  totals.TotalAmount,
	}
	if h.lottery != nil {
		rt := h.lottery.Get()
		payload["daily_budget"] = rt.DailyBudget
		if rt.DailyBudget > 0 {
			left := rt.DailyBudget - todayAmount
			if left < 0 {
				left = 0
			}
			payload["budget_left"] = left
		}
	}
	writeJSON(w, http.StatusOK, payload)
}
