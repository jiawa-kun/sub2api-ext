package handler

import (
	"net/http"
	"strings"

	"sub2api-ext/internal/store"
)

// AdminOverview GET /api/admin/overview — one-shot ops snapshot for the admin home.
func (h *Handler) AdminOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, err := h.requireAdmin(r); err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if !h.limitAdminRead.Allow("AdminOverview:" + clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "rate limited")
		return
	}

	today := h.settings.Today()
	out := map[string]any{
		"today": today,
		"links": map[string]string{
			"ledger":   "./admin.html#ledger",
			"campaign": "./admin.html#campaign",
			"patrol":   "./admin.html#patrol",
			"checkin":  "./admin.html#checkin",
			"lottery":  "./admin.html#lottery",
			"report":   "./admin.html#report",
			"tasks":    "./admin.html#tasks",
			"notify":   "./admin.html#notify",
		},
	}

	// Ledger today by status
	ledSum, err := h.store.SummarizeLedgerByStatus(r.Context(), store.LedgerFilter{From: today, To: today})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "ledger: "+err.Error())
		return
	}
	out["ledger_today"] = map[string]any{
		"available":      true,
		"success_count":  ledSum.SuccessCount,
		"success_amount": ledSum.SuccessAmount,
		"failed_count":   ledSum.FailedCount,
		"skipped_count":  ledSum.SkippedCount,
	}

	// Check-in budget
	rt := h.settings.Get()
	st, err := h.store.StatsByDate(r.Context(), today)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "checkin stats: "+err.Error())
		return
	}
	var checkinRemaining any
	if rt.DailyBudget > 0 {
		rem := rt.DailyBudget - st.TotalAmount
		if rem < 0 {
			rem = 0
		}
		checkinRemaining = rem
	}
	out["checkin"] = map[string]any{
		"available":       true,
		"enabled":         rt.Enabled,
		"count":           st.Count,
		"total_amount":    st.TotalAmount,
		"daily_budget":    rt.DailyBudget,
		"daily_remaining": checkinRemaining,
	}

	// Lottery
	if h.lottery != nil {
		lrt := h.lottery.Get()
		spent, _ := h.store.SumLotteryAmountByDate(r.Context(), today)
		var lotRemaining any
		if lrt.DailyBudget > 0 {
			rem := lrt.DailyBudget - spent
			if rem < 0 {
				rem = 0
			}
			lotRemaining = rem
		}
		out["lottery"] = map[string]any{
			"available":       true,
			"enabled":         lrt.Enabled,
			"today_amount":    spent,
			"daily_budget":    lrt.DailyBudget,
			"daily_remaining": lotRemaining,
		}
	} else {
		out["lottery"] = map[string]any{"available": false}
	}

	// Campaigns
	camps, err := h.store.ListRankCampaigns(r.Context(), 50)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "campaigns: "+err.Error())
		return
	}
	active, partial := 0, 0
	recent := make([]map[string]any, 0, 5)
	for _, c := range camps {
		switch c.Status {
		case store.CampaignStatusActive, store.CampaignStatusDraft:
			if c.Status == store.CampaignStatusActive {
				active++
			}
		case store.CampaignStatusPartial:
			partial++
		}
		if len(recent) < 5 {
			recent = append(recent, map[string]any{
				"id": c.ID, "name": c.Name, "board": c.Board, "status": c.Status,
				"start_date": c.StartDate, "end_date": c.EndDate,
			})
		}
	}
	// Also count active via date window for accuracy
	activeList, _ := h.store.ListActiveRankCampaigns(r.Context(), today)
	out["campaigns"] = map[string]any{
		"available":     true,
		"active_count":  len(activeList),
		"partial_count": partial,
		"draft_or_listed_active": active,
		"recent":        recent,
	}

	// Patrol problem accounts
	problem, err := h.store.CountPatrolAccountStates(r.Context(), true)
	if err != nil {
		// table may be empty/missing on fresh DB — treat as zero available
		if strings.Contains(err.Error(), "no such table") {
			out["patrol"] = map[string]any{"available": false, "problem_accounts": 0}
		} else {
			writeErr(w, http.StatusInternalServerError, "patrol: "+err.Error())
			return
		}
	} else {
		out["patrol"] = map[string]any{
			"available":        true,
			"problem_accounts": problem,
		}
	}

	writeJSON(w, http.StatusOK, out)
}
