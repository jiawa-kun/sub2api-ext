package handler

import (
	"context"
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
		if board != store.CampaignBoardRewards && board != store.CampaignBoardConsumption {
			writeErr(w, http.StatusBadRequest, "board must be rewards or consumption")
			return
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
	if action == "cancel" && r.Method == http.MethodPost {
		h.adminCancelCampaign(w, r, id)
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
		if c.Status == store.CampaignStatusCancelled {
			writeErr(w, http.StatusBadRequest, "cancelled campaign is immutable")
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

func (h *Handler) adminCancelCampaign(w http.ResponseWriter, r *http.Request, id int64) {
	c, err := h.store.GetRankCampaign(r.Context(), id)
	if err != nil || c == nil {
		writeErr(w, http.StatusNotFound, "campaign not found")
		return
	}
	switch c.Status {
	case store.CampaignStatusCancelled:
		writeJSON(w, http.StatusOK, map[string]any{
			"campaign_id": c.ID, "status": c.Status, "message": "already cancelled",
		})
		return
	case store.CampaignStatusSettled:
		writeErr(w, http.StatusBadRequest, "settled campaign cannot be cancelled")
		return
	case store.CampaignStatusDraft, store.CampaignStatusActive, store.CampaignStatusPartial:
		// ok
	default:
		writeErr(w, http.StatusBadRequest, "campaign status not cancellable: "+c.Status)
		return
	}
	// Cancel only stops further settle/display; already-granted awards are not clawed back.
	if err := h.store.MarkCampaignStatus(r.Context(), c.ID, store.CampaignStatusCancelled, false); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	c2, _ := h.store.GetRankCampaign(r.Context(), id)
	out := campaignJSON(*c)
	if c2 != nil {
		out = campaignJSON(*c2)
	}
	out["message"] = "cancelled"
	if c.Status == store.CampaignStatusPartial {
		out["note"] = "already granted awards were not reversed; check ledger/awards for paid rows"
	}
	writeJSON(w, http.StatusOK, out)
}


type campaignRankRow struct {
	UserID int64
	Amount float64
}


// validateCampaignMeta checks board/status/date/reward rules before preview or settle.
// requireSettleable=true enforces draft/active/partial status.
func validateCampaignMeta(c *store.RankCampaign, requireSettleable bool) error {
	if c == nil {
		return fmt.Errorf("campaign required")
	}
	if c.Board != store.CampaignBoardRewards && c.Board != store.CampaignBoardConsumption && c.Board != "" {
		return fmt.Errorf("board must be rewards or consumption")
	}
	board := c.Board
	if board == "" {
		board = store.CampaignBoardRewards
	}
	if board != store.CampaignBoardRewards && board != store.CampaignBoardConsumption {
		return fmt.Errorf("board must be rewards or consumption")
	}
	if requireSettleable {
		if c.Status == store.CampaignStatusSettled {
			return fmt.Errorf("already settled")
		}
		if c.Status == store.CampaignStatusCancelled {
			return fmt.Errorf("campaign status not settleable: %s", c.Status)
		}
		if c.Status != store.CampaignStatusActive && c.Status != store.CampaignStatusPartial && c.Status != store.CampaignStatusDraft {
			return fmt.Errorf("campaign status not settleable: %s", c.Status)
		}
	}
	start := strings.TrimSpace(c.StartDate)
	end := strings.TrimSpace(c.EndDate)
	if start == "" || end == "" {
		return fmt.Errorf("start_date/end_date required")
	}
	if start > end {
		return fmt.Errorf("start_date must be <= end_date")
	}
	rules, err := c.ParseRewards()
	if err != nil {
		return fmt.Errorf("invalid rewards json")
	}
	if !hasPositiveRewardRule(rules) {
		return fmt.Errorf("rewards must include at least one positive amount")
	}
	return nil
}

func hasPositiveRewardRule(rules []store.RankRewardRule) bool {
	for _, r := range rules {
		if r.Amount > 0 {
			return true
		}
	}
	return false
}

// planCampaignAwards computes payable/skipped counts for preview and settle gates.
func planCampaignAwards(c *store.RankCampaign, rows []campaignRankRow, rules []store.RankRewardRule, existing map[int64]store.RankCampaignAward) (payable, skipped int, plannedSpend float64, details []map[string]any) {
	if existing == nil {
		existing = map[int64]store.RankCampaignAward{}
	}
	details = make([]map[string]any, 0, len(rows))
	spentPlan := 0.0
	for i, row := range rows {
		rank := i + 1
		amt := store.AmountForRank(rules, rank)
		status := "planned"
		if amt <= 0 {
			status = "no_reward"
			skipped++
		} else if c != nil && c.BudgetCap > 0 && spentPlan+amt > c.BudgetCap {
			status = "budget_cut"
			skipped++
		} else if prev, ok := existing[row.UserID]; ok {
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
		details = append(details, map[string]any{
			"user_id": row.UserID, "rank": rank, "amount": amt, "status": status,
			"board_amount": row.Amount,
		})
	}
	return payable, skipped, spentPlan, details
}

// validateCampaignSettleReady enforces non-empty ranking and at least one payable award.
func validateCampaignSettleReady(rows []campaignRankRow, payable int) error {
	if len(rows) == 0 {
		return fmt.Errorf("ranking is empty, nothing to settle")
	}
	if payable <= 0 {
		return fmt.Errorf("no payable awards (all skipped or zero reward)")
	}
	return nil
}


func (h *Handler) loadCampaignRankRows(ctx context.Context, c *store.RankCampaign, topN int) ([]campaignRankRow, error) {
	if c == nil {
		return nil, fmt.Errorf("campaign required")
	}
	if topN <= 0 {
		topN = 10
	}
	switch c.Board {
	case store.CampaignBoardRewards, "":
		rows, _, err := h.store.ListRewardRanking(ctx, c.StartDate, c.EndDate, topN)
		if err != nil {
			return nil, err
		}
		out := make([]campaignRankRow, 0, len(rows))
		for _, row := range rows {
			out = append(out, campaignRankRow{UserID: row.UserID, Amount: row.TotalAmount})
		}
		return out, nil
	case store.CampaignBoardConsumption:
		h.syncAdminCred()
		if h.effectiveAdminCred() == "" {
			return nil, fmt.Errorf("admin api key required for consumption ranking")
		}
		res, err := h.client.FetchUsageRanking(ctx, "", sub2api.ClientMeta{}, sub2api.UsageRankQuery{
			FromDate: c.StartDate,
			ToDate:   c.EndDate,
			Limit:    topN,
		})
		if err != nil {
			return nil, err
		}
		if res == nil {
			return nil, nil
		}
		out := make([]campaignRankRow, 0, len(res.Items))
		for _, it := range res.Items {
			uid := it.UserID
			if uid <= 0 {
				continue
			}
			out = append(out, campaignRankRow{UserID: uid, Amount: it.Amount})
		}
		if len(out) > topN {
			out = out[:topN]
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported board: %s", c.Board)
	}
}

func (h *Handler) adminPreviewCampaign(w http.ResponseWriter, r *http.Request, id int64) {
	c, err := h.store.GetRankCampaign(r.Context(), id)
	if err != nil || c == nil {
		writeErr(w, http.StatusNotFound, "campaign not found")
		return
	}
	if err := validateCampaignMeta(c, false); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
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
	rows, err := h.loadCampaignRankRows(r.Context(), c, topN)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "load ranking failed: "+err.Error())
		return
	}
	existing, _ := h.store.CampaignAwardMap(r.Context(), c.ID)
	payable, skipped, spentPlan, details := planCampaignAwards(c, rows, rules, existing)
	warning := ""
	if len(rows) == 0 {
		warning = "ranking is empty"
	} else if payable <= 0 {
		warning = "no payable awards"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"campaign_id": c.ID, "board": c.Board, "top_n": topN,
		"payable": payable, "skipped": skipped, "planned_spend": spentPlan,
		"budget_cap": c.BudgetCap, "details": details, "row_count": len(rows),
		"warning": warning,
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
	if err := validateCampaignMeta(c, true); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
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
	rows, err := h.loadCampaignRankRows(r.Context(), c, topN)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "load ranking failed: "+err.Error())
		return
	}
	existing, err := h.store.CampaignAwardMap(r.Context(), c.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Gate empty / non-payable settles up front (preview still allowed).
	payable, _, _, _ := planCampaignAwards(c, rows, rules, existing)
	if err := validateCampaignSettleReady(rows, payable); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
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
