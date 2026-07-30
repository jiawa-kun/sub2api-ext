package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"sub2api-ext/internal/credit"
	"sub2api-ext/internal/store"
	"sub2api-ext/internal/sub2api"
)

type campaignBody struct {
	Name       string  `json:"name"`
	Board      string  `json:"board"`
	StartDate  string  `json:"start_date"`
	EndDate    string  `json:"end_date"`
	TopN       int     `json:"top_n"`
	Rewards    any     `json:"rewards"`
	BudgetCap  float64 `json:"budget_cap"`
	Status     string  `json:"status"`
}

func campaignJSON(c store.RankCampaign) map[string]any {
	var rewards any
	_ = json.Unmarshal([]byte(c.RewardsJSON), &rewards)
	return map[string]any{
		"id": c.ID, "name": c.Name, "board": c.Board,
		"start_date": c.StartDate, "end_date": c.EndDate, "top_n": c.TopN,
		"rewards": rewards, "budget_cap": c.BudgetCap, "status": c.Status,
		"settled_at": c.SettledAt, "created_at": c.CreatedAt, "updated_at": c.UpdatedAt,
	}
}

// AdminRankCampaigns GET list / POST create
func (h *Handler) AdminRankCampaigns(w http.ResponseWriter, r *http.Request) {
	if _, err := h.requireAdmin(r); err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		list, err := h.store.ListRankCampaigns(r.Context(), 100)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		items := make([]map[string]any, 0, len(list))
		for _, c := range list {
			items = append(items, campaignJSON(c))
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	case http.MethodPost:
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var in campaignBody
		if err := json.Unmarshal(body, &in); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		if strings.TrimSpace(in.Name) == "" || in.StartDate == "" || in.EndDate == "" {
			writeErr(w, http.StatusBadRequest, "name/start_date/end_date required")
			return
		}
		board := strings.TrimSpace(in.Board)
		if board == "" {
			board = store.CampaignBoardRewards
		}
		if board != store.CampaignBoardRewards {
			// MVP: only rewards board is settleable; still allow create for display-only consumption later
			if board != store.CampaignBoardConsumption {
				writeErr(w, http.StatusBadRequest, "board must be rewards or consumption")
				return
			}
		}
		rj, _ := json.Marshal(in.Rewards)
		if in.Rewards == nil {
			rj = []byte(`[]`)
		}
		st := strings.TrimSpace(in.Status)
		if st == "" {
			st = store.CampaignStatusDraft
		}
		id, err := h.store.CreateRankCampaign(r.Context(), store.RankCampaign{
			Name: in.Name, Board: board, StartDate: in.StartDate, EndDate: in.EndDate,
			TopN: in.TopN, RewardsJSON: string(rj), BudgetCap: in.BudgetCap, Status: st,
		})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		c, _ := h.store.GetRankCampaign(r.Context(), id)
		writeJSON(w, http.StatusOK, campaignJSON(*c))
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// AdminRankCampaignByID PUT update / GET one
func (h *Handler) AdminRankCampaignByID(w http.ResponseWriter, r *http.Request) {
	if _, err := h.requireAdmin(r); err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	// path: /api/admin/rank/campaigns/{id} or .../settle or .../awards
	path := r.URL.Path
	// find id segment after campaigns/
	idx := strings.LastIndex(path, "/campaigns/")
	if idx < 0 {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	rest := strings.TrimPrefix(path[idx+len("/campaigns/"):], "/")
	parts := strings.Split(rest, "/")
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	if action == "settle" && r.Method == http.MethodPost {
		h.adminSettleCampaign(w, r, id)
		return
	}
	if action == "preview" && (r.Method == http.MethodGet || r.Method == http.MethodPost) {
		h.adminPreviewCampaign(w, r, id)
		return
	}
	if action == "awards" && r.Method == http.MethodGet {
		list, err := h.store.ListCampaignAwards(r.Context(), id)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		items := make([]map[string]any, 0, len(list))
		for _, a := range list {
			items = append(items, map[string]any{
				"id": a.ID, "campaign_id": a.CampaignID, "user_id": a.UserID,
				"rank": a.Rank, "amount": a.Amount, "ledger_id": a.LedgerID,
				"status": a.Status, "created_at": a.CreatedAt,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
		return
	}

	c, err := h.store.GetRankCampaign(r.Context(), id)
	if err != nil || c == nil {
		writeErr(w, http.StatusNotFound, "campaign not found")
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, campaignJSON(*c))
		return
	}
	if r.Method == http.MethodPut || r.Method == http.MethodPost {
		if c.Status == store.CampaignStatusSettled {
			writeErr(w, http.StatusBadRequest, "settled campaign is immutable")
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var in campaignBody
		if err := json.Unmarshal(body, &in); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		if in.Name != "" {
			c.Name = in.Name
		}
		if in.Board != "" {
			c.Board = in.Board
		}
		if in.StartDate != "" {
			c.StartDate = in.StartDate
		}
		if in.EndDate != "" {
			c.EndDate = in.EndDate
		}
		if in.TopN > 0 {
			c.TopN = in.TopN
		}
		if in.Rewards != nil {
			rj, _ := json.Marshal(in.Rewards)
			c.RewardsJSON = string(rj)
		}
		if in.Status != "" {
			c.Status = in.Status
		}
		c.BudgetCap = in.BudgetCap
		if err := h.store.UpdateRankCampaign(r.Context(), *c); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		c2, _ := h.store.GetRankCampaign(r.Context(), id)
		writeJSON(w, http.StatusOK, campaignJSON(*c2))
		return
	}
	writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (h *Handler) adminPreviewCampaign(w http.ResponseWriter, r *http.Request, id int64) {
	c, err := h.store.GetRankCampaign(r.Context(), id)
	if err != nil || c == nil {
		writeErr(w, http.StatusNotFound, "campaign not found")
		return
	}
	if c.Board != store.CampaignBoardRewards {
		writeErr(w, http.StatusBadRequest, "MVP only previews rewards board campaigns")
		return
	}
	rules, err := c.ParseRewards()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid rewards json")
		return
	}
	topN := c.TopN
	if topN <= 0 {
		topN = 10
	}
	rows, _, err := h.store.ListRewardRanking(r.Context(), c.StartDate, c.EndDate, topN)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	existing, _ := h.store.CampaignAwardMap(r.Context(), c.ID)
	spentPlan := 0.0
	payable := 0
	skipped := 0
	details := make([]map[string]any, 0, len(rows))
	for i, row := range rows {
		rank := i + 1
		amt := store.AmountForRank(rules, rank)
		status := "planned"
		if amt <= 0 {
			status = "no_reward"
			skipped++
		} else if c.BudgetCap > 0 && spentPlan+amt > c.BudgetCap {
			status = "budget_cut"
			skipped++
		} else {
			if prev, ok := existing[row.UserID]; ok {
				if prev.Status == "success" || prev.Status == "skipped" {
					status = "already_" + prev.Status
					skipped++
				} else {
					status = "retry_" + prev.Status
					spentPlan += amt
					payable++
				}
			} else {
				spentPlan += amt
				payable++
			}
		}
		details = append(details, map[string]any{
			"user_id": row.UserID, "rank": rank, "amount": amt, "status": status,
			"board_amount": row.TotalAmount,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"campaign_id": c.ID, "board": c.Board, "top_n": topN,
		"payable": payable, "skipped": skipped, "planned_spend": spentPlan,
		"budget_cap": c.BudgetCap, "details": details,
	})
}

func (h *Handler) adminSettleCampaign(w http.ResponseWriter, r *http.Request, id int64) {
	if h.credit == nil {
		writeErr(w, http.StatusServiceUnavailable, "credit service unavailable")
		return
	}
	h.syncAdminCred()
	if h.effectiveAdminCred() == "" {
		writeErr(w, http.StatusServiceUnavailable, "admin api key required")
		return
	}
	c, err := h.store.GetRankCampaign(r.Context(), id)
	if err != nil || c == nil {
		writeErr(w, http.StatusNotFound, "campaign not found")
		return
	}
	if c.Status == store.CampaignStatusSettled {
		writeErr(w, http.StatusBadRequest, "already settled")
		return
	}
	if c.Status != store.CampaignStatusActive && c.Status != store.CampaignStatusPartial && c.Status != store.CampaignStatusDraft {
		writeErr(w, http.StatusBadRequest, "campaign status not settleable: "+c.Status)
		return
	}
	if c.Board != store.CampaignBoardRewards {
		writeErr(w, http.StatusBadRequest, "MVP only settles rewards board campaigns")
		return
	}
	rules, err := c.ParseRewards()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid rewards json")
		return
	}
	topN := c.TopN
	if topN <= 0 {
		topN = 10
	}
	rows, _, err := h.store.ListRewardRanking(r.Context(), c.StartDate, c.EndDate, topN)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	existing, err := h.store.CampaignAwardMap(r.Context(), c.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Count already-success spend toward budget so retries respect cap.
	spent := 0.0
	for _, a := range existing {
		if a.Status == "success" {
			spent += a.Amount
		}
	}

	awarded := 0
	skipped := 0
	failed := 0
	details := make([]map[string]any, 0, len(rows))
	for i, row := range rows {
		rank := i + 1
		amt := store.AmountForRank(rules, rank)
		if amt <= 0 {
			skipped++
			details = append(details, map[string]any{"user_id": row.UserID, "rank": rank, "status": "no_reward", "amount": 0})
			continue
		}
		if prev, ok := existing[row.UserID]; ok && (prev.Status == "success" || prev.Status == "skipped") {
			skipped++
			details = append(details, map[string]any{
				"user_id": row.UserID, "rank": rank, "amount": prev.Amount,
				"status": "already_" + prev.Status, "ledger_id": prev.LedgerID,
			})
			continue
		}
		if c.BudgetCap > 0 && spent+amt > c.BudgetCap {
			skipped++
			details = append(details, map[string]any{"user_id": row.UserID, "rank": rank, "status": "budget_cut", "amount": amt})
			continue
		}

		res, err := h.credit.Grant(r.Context(), credit.Request{
			UserID: row.UserID, Amount: amt, Source: credit.SourceRankReward,
			SourceRef: fmt.Sprintf("campaign:%d:rank:%d", c.ID, rank),
			Scope:     sub2api.IdempotencyScopeRankReward,
			Slot:      fmt.Sprintf("c%d-r%d-u%d", c.ID, rank, row.UserID),
			Notes:     fmt.Sprintf("rank-campaign:%s:#%d", c.Name, rank),
		})
		status := "success"
		ledgerID := int64(0)
		if err != nil {
			failed++
			status = "failed"
			details = append(details, map[string]any{"user_id": row.UserID, "rank": rank, "amount": amt, "status": status, "error": err.Error()})
			if prev, ok := existing[row.UserID]; ok {
				_ = h.store.UpdateCampaignAward(r.Context(), prev.ID, amt, 0, status)
			} else {
				_, _ = h.store.InsertCampaignAward(r.Context(), store.RankCampaignAward{
					CampaignID: c.ID, UserID: row.UserID, Rank: rank, Amount: amt, Status: status,
				})
			}
			continue
		}
		if res != nil {
			ledgerID = res.LedgerID
			if res.Skipped {
				status = "skipped"
				skipped++
			} else {
				awarded++
				spent += amt
			}
		}
		if prev, ok := existing[row.UserID]; ok {
			_ = h.store.UpdateCampaignAward(r.Context(), prev.ID, amt, ledgerID, status)
		} else {
			_, _ = h.store.InsertCampaignAward(r.Context(), store.RankCampaignAward{
				CampaignID: c.ID, UserID: row.UserID, Rank: rank, Amount: amt, LedgerID: ledgerID, Status: status,
			})
		}
		details = append(details, map[string]any{"user_id": row.UserID, "rank": rank, "amount": amt, "status": status, "ledger_id": ledgerID})
	}

	finalStatus := store.CampaignStatusSettled
	if failed > 0 {
		finalStatus = store.CampaignStatusPartial
		_ = h.store.MarkCampaignStatus(r.Context(), c.ID, finalStatus, false)
	} else {
		_ = h.store.MarkCampaignSettled(r.Context(), c.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"campaign_id": c.ID, "status": finalStatus,
		"awarded": awarded, "skipped": skipped, "failed": failed,
		"spent": spent, "details": details,
	})
}

// PublicRankCampaigns GET /api/ranking/campaigns
func (h *Handler) PublicRankCampaigns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	today := h.settings.Today()
	list, err := h.store.ListActiveRankCampaigns(r.Context(), today)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]map[string]any, 0, len(list))
	for _, c := range list {
		items = append(items, campaignJSON(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
