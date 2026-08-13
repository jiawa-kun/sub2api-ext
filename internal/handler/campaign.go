package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"sub2api-ext/internal/credit"
	"sub2api-ext/internal/store"
	"sub2api-ext/internal/sub2api"
)

type campaignBody struct {
	Name           string  `json:"name"`
	Board          string  `json:"board"`
	StartDate      string  `json:"start_date"`
	EndDate        string  `json:"end_date"`
	Frequency      string  `json:"frequency"`
	SettlementTime string  `json:"settlement_time"`
	TopN           int     `json:"top_n"`
	Rewards        any     `json:"rewards"`
	BudgetCap      float64 `json:"budget_cap"`
	Status         string  `json:"status"`
}

func campaignJSON(c store.RankCampaign) map[string]any {
	var rewards any
	_ = json.Unmarshal([]byte(c.RewardsJSON), &rewards)
	return map[string]any{
		"id": c.ID, "name": c.Name, "board": c.Board,
		"start_date": c.StartDate, "end_date": c.EndDate, "top_n": c.TopN,
		"frequency": c.Frequency, "settlement_time": c.SettlementTime,
		"rewards": rewards, "budget_cap": c.BudgetCap, "status": c.Status,
		"settled_at": c.SettledAt, "created_at": c.CreatedAt, "updated_at": c.UpdatedAt,
		"award_count": c.AwardCount,
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
		if !h.limitAdminRead.Allow("campaign-r:" + clientIP(r)) {
			writeErr(w, http.StatusTooManyRequests, "rate limited")
			return
		}
		page := queryInt(r, "page", 1)
		pageSize := queryInt(r, "page_size", 10)
		if page < 1 {
			page = 1
		}
		if pageSize < 1 {
			pageSize = 10
		}
		if pageSize > 100 {
			pageSize = 100
		}
		filter := store.RankCampaignFilter{
			Keyword:   strings.TrimSpace(r.URL.Query().Get("keyword")),
			Board:     strings.TrimSpace(r.URL.Query().Get("board")),
			Frequency: strings.TrimSpace(r.URL.Query().Get("frequency")),
			Status:    strings.TrimSpace(r.URL.Query().Get("status")),
			Limit:     pageSize, Offset: (page - 1) * pageSize,
		}
		if filter.Board != "" && filter.Board != store.CampaignBoardRewards && filter.Board != store.CampaignBoardConsumption {
			writeErr(w, http.StatusBadRequest, "invalid board filter")
			return
		}
		if filter.Frequency != "" && filter.Frequency != store.CampaignFrequencyOnce && filter.Frequency != store.CampaignFrequencyDaily && filter.Frequency != store.CampaignFrequencyWeekly && filter.Frequency != store.CampaignFrequencyMonthly {
			writeErr(w, http.StatusBadRequest, "invalid frequency filter")
			return
		}
		if filter.Status != "" && filter.Status != store.CampaignStatusDraft && filter.Status != store.CampaignStatusActive && filter.Status != store.CampaignStatusPartial && filter.Status != store.CampaignStatusSettled && filter.Status != store.CampaignStatusCancelled {
			writeErr(w, http.StatusBadRequest, "invalid status filter")
			return
		}
		list, total, err := h.store.ListRankCampaignsPage(r.Context(), filter)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		items := make([]map[string]any, 0, len(list))
		for _, c := range list {
			items = append(items, campaignJSON(c))
		}
		totalPages := 0
		if total > 0 {
			totalPages = (total + pageSize - 1) / pageSize
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "page": page, "page_size": pageSize, "total_pages": totalPages})
	case http.MethodPost:
		if !h.limitAdminWrite.Allow("campaign-w:" + clientIP(r)) {
			writeErr(w, http.StatusTooManyRequests, "rate limited")
			return
		}
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
		if st != store.CampaignStatusDraft && st != store.CampaignStatusActive {
			writeErr(w, http.StatusBadRequest, "status must be draft or active")
			return
		}
		candidate := store.RankCampaign{
			Name: in.Name, Board: board, StartDate: in.StartDate, EndDate: in.EndDate,
			Frequency: in.Frequency, SettlementTime: in.SettlementTime,
			TopN: in.TopN, RewardsJSON: string(rj), BudgetCap: in.BudgetCap, Status: st,
		}
		if err := validateCampaignMeta(&candidate, false); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		id, err := h.store.CreateRankCampaign(r.Context(), candidate)
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

	writeAction := action == "settle" || action == "cancel" || r.Method == http.MethodPut || r.Method == http.MethodDelete || (r.Method == http.MethodPost && action == "")
	// preview uses POST or GET but is read-only planning
	if action == "preview" {
		writeAction = false
	}
	if writeAction {
		if !h.limitAdminWrite.Allow("campaign-w:" + clientIP(r)) {
			writeErr(w, http.StatusTooManyRequests, "rate limited")
			return
		}
	} else {
		if !h.limitAdminRead.Allow("campaign-r:" + clientIP(r)) {
			writeErr(w, http.StatusTooManyRequests, "rate limited")
			return
		}
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
		page := queryInt(r, "page", 1)
		pageSize := queryInt(r, "page_size", 10)
		if page < 1 {
			page = 1
		}
		if pageSize < 1 {
			pageSize = 10
		}
		if pageSize > 100 {
			pageSize = 100
		}
		status := strings.TrimSpace(r.URL.Query().Get("status"))
		if status != "" && status != "success" && status != "failed" && status != "skipped" {
			writeErr(w, http.StatusBadRequest, "invalid award status filter")
			return
		}
		userID := int64(queryInt(r, "user_id", 0))
		list, total, summary, err := h.store.ListCampaignAwardsPage(r.Context(), id, store.RankCampaignAwardFilter{
			PeriodKey: strings.TrimSpace(r.URL.Query().Get("period_key")), Status: status, UserID: userID,
			Limit: pageSize, Offset: (page - 1) * pageSize,
		})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		items := make([]map[string]any, 0, len(list))
		for _, a := range list {
			items = append(items, map[string]any{
				"id": a.ID, "campaign_id": a.CampaignID, "user_id": a.UserID,
				"period_key": a.PeriodKey, "rank": a.Rank, "amount": a.Amount, "ledger_id": a.LedgerID,
				"status": a.Status, "error": a.Error, "created_at": a.CreatedAt,
			})
		}
		totalPages := 0
		if total > 0 {
			totalPages = (total + pageSize - 1) / pageSize
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"items": items, "total": total, "page": page, "page_size": pageSize, "total_pages": totalPages,
			"summary": map[string]any{"total_amount": summary.TotalAmount, "success": summary.SuccessCount, "failed": summary.FailedCount, "skipped": summary.SkippedCount},
		})
		return
	}

	c, err := h.store.GetRankCampaign(r.Context(), id)
	if err != nil || c == nil {
		writeErr(w, http.StatusNotFound, "campaign not found")
		return
	}
	count, countErr := h.store.CampaignAwardCount(r.Context(), c.ID)
	if countErr == nil {
		c.AwardCount = count
	}
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, campaignJSON(*c))
		return
	}
	if r.Method == http.MethodDelete && action == "" {
		deleted, err := h.store.DeleteRankCampaignIfNoAwards(r.Context(), c.ID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !deleted {
			writeErr(w, http.StatusConflict, "campaign has award records and can only be cancelled")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "campaign_id": c.ID})
		return
	}
	if r.Method == http.MethodPut || r.Method == http.MethodPost {
		if c.Status != store.CampaignStatusDraft && c.Status != store.CampaignStatusActive {
			writeErr(w, http.StatusBadRequest, "only draft or active campaigns can be edited")
			return
		}
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
		if in.Frequency != "" {
			c.Frequency = in.Frequency
		}
		if in.SettlementTime != "" {
			c.SettlementTime = in.SettlementTime
		}
		if in.TopN > 0 {
			c.TopN = in.TopN
		}
		if in.Rewards != nil {
			rj, _ := json.Marshal(in.Rewards)
			c.RewardsJSON = string(rj)
		}
		if in.Status != "" {
			if in.Status != store.CampaignStatusDraft && in.Status != store.CampaignStatusActive {
				writeErr(w, http.StatusBadRequest, "status must be draft or active")
				return
			}
			c.Status = in.Status
		}
		c.BudgetCap = in.BudgetCap
		if err := validateCampaignMeta(c, false); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
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
	UserID              int64
	Amount              float64
	RequestCount        int64
	ActualCost          float64
	TokenShare          float64
	AvgTokensPerRequest float64
	CostPerRequest      float64
	CostPerMillionToken float64
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
	c.Frequency = store.NormalizeCampaignFrequency(c.Frequency)
	if c.SettlementTime == "" {
		c.SettlementTime = store.NormalizeCampaignSettlementTime(c.SettlementTime)
	}
	if err := store.ValidateCampaignSettlementTime(c.SettlementTime); err != nil {
		return err
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
	if _, err := time.Parse("2006-01-02", start); err != nil {
		return fmt.Errorf("start_date must use YYYY-MM-DD")
	}
	if _, err := time.Parse("2006-01-02", end); err != nil {
		return fmt.Errorf("end_date must use YYYY-MM-DD")
	}
	if start > end {
		return fmt.Errorf("start_date must be <= end_date")
	}
	rules, err := c.ParseRewards()
	if err != nil {
		return fmt.Errorf("invalid rewards json")
	}
	if err := validateRewardRules(rules); err != nil {
		return err
	}
	return nil
}

func validateRewardRules(rules []store.RankRewardRule) error {
	if len(rules) == 0 {
		return fmt.Errorf("rewards must include at least one rule")
	}
	type rewardRange struct {
		from, to int
		exact    bool
	}
	occupied := make([]rewardRange, 0, len(rules))
	for i, rule := range rules {
		if rule.Amount <= 0 {
			return fmt.Errorf("rewards must include positive amounts; reward rule %d amount must be greater than 0", i+1)
		}
		from, to := rule.Rank, rule.Rank
		if rule.Rank < 0 {
			return fmt.Errorf("reward rule %d has invalid rank", i+1)
		}
		if rule.Rank != 0 && (rule.RankFrom != 0 || rule.RankTo != 0) {
			return fmt.Errorf("reward rule %d cannot mix rank and rank range", i+1)
		}
		if rule.Rank <= 0 {
			if rule.RankFrom <= 0 || rule.RankTo < rule.RankFrom {
				return fmt.Errorf("reward rule %d has invalid rank range", i+1)
			}
			from, to = rule.RankFrom, rule.RankTo
		}
		occupied = append(occupied, rewardRange{from: from, to: to, exact: rule.Rank > 0})
	}
	for i := 0; i < len(occupied); i++ {
		for j := i + 1; j < len(occupied); j++ {
			if occupied[i].exact && occupied[j].exact && occupied[i].from == occupied[j].from {
				return fmt.Errorf("reward rules %d and %d duplicate exact rank", i+1, j+1)
			}
			if !occupied[i].exact && !occupied[j].exact && occupied[i].from <= occupied[j].to && occupied[j].from <= occupied[i].to {
				return fmt.Errorf("reward rules %d and %d have overlapping ranks", i+1, j+1)
			}
		}
	}
	return nil
}

func campaignPeriod(c *store.RankCampaign, requested string, now time.Time) (store.CampaignPeriod, error) {
	if c == nil {
		return store.CampaignPeriod{}, fmt.Errorf("campaign required")
	}
	frequency := store.NormalizeCampaignFrequency(c.Frequency)
	loc, err := time.LoadLocation(store.CampaignTimezone)
	if err != nil {
		return store.CampaignPeriod{}, err
	}
	if frequency == store.CampaignFrequencyOnce {
		return store.CampaignPeriod{Key: store.CampaignFrequencyOnce, StartDate: c.StartDate, EndDate: c.EndDate}, nil
	}
	requested = strings.TrimSpace(requested)
	var period store.CampaignPeriod
	if requested == "" {
		period, err = store.PreviousCampaignPeriod(frequency, now, loc)
	} else {
		period, err = store.CampaignPeriodFromKey(frequency, requested, loc)
	}
	if err != nil {
		return store.CampaignPeriod{}, err
	}
	if !store.PeriodInsideCampaign(period, c.StartDate, c.EndDate) {
		return store.CampaignPeriod{}, fmt.Errorf("period %s is outside campaign date range", period.Key)
	}
	return period, nil
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
	for _, prev := range existing {
		if prev.Status == "success" {
			spentPlan += prev.Amount
		}
	}
	for i, row := range rows {
		rank := i + 1
		amt := store.AmountForRank(rules, rank)
		status := "planned"
		if amt <= 0 {
			status = "no_reward"
			skipped++
		} else if prev, ok := existing[row.UserID]; ok {
			if prev.Status == "success" || prev.Status == "skipped" {
				status = "already_" + prev.Status
				skipped++
			} else if c != nil && c.BudgetCap > 0 && spentPlan+amt > c.BudgetCap {
				status = "budget_cut"
				skipped++
			} else {
				status = "retry_" + prev.Status
				spentPlan += amt
				payable++
			}
		} else if c != nil && c.BudgetCap > 0 && spentPlan+amt > c.BudgetCap {
			status = "budget_cut"
			skipped++
		} else {
			spentPlan += amt
			payable++
		}
		details = append(details, map[string]any{
			"user_id": row.UserID, "rank": rank, "amount": amt, "status": status,
			"board_amount": row.Amount, "request_count": row.RequestCount,
			"actual_cost": row.ActualCost, "token_share": row.TokenShare,
			"avg_tokens_per_request":  row.AvgTokensPerRequest,
			"cost_per_request":        row.CostPerRequest,
			"cost_per_million_tokens": row.CostPerMillionToken,
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

func (h *Handler) loadCampaignRankRows(ctx context.Context, c *store.RankCampaign, period store.CampaignPeriod, topN int) ([]campaignRankRow, error) {
	if c == nil {
		return nil, fmt.Errorf("campaign required")
	}
	if topN <= 0 {
		topN = 10
	}
	switch c.Board {
	case store.CampaignBoardRewards, "":
		rows, _, err := h.store.ListRewardRanking(ctx, period.StartDate, period.EndDate, topN)
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
			return nil, fmt.Errorf("admin api key required for token usage ranking")
		}
		res, err := h.client.FetchTokenUsageRanking(ctx, sub2api.UsageRankQuery{
			FromDate: period.StartDate,
			ToDate:   period.EndDate,
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
			out = append(out, campaignRankRow{
				UserID: uid, Amount: it.TokenCount, RequestCount: it.RequestCount,
				ActualCost: it.Amount, TokenShare: it.TokenShare,
				AvgTokensPerRequest: it.AvgTokensPerRequest,
				CostPerRequest:      it.CostPerRequest,
				CostPerMillionToken: it.CostPerMillionTokens,
			})
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
	requestedPeriod := strings.TrimSpace(r.URL.Query().Get("period_key"))
	if r.Method == http.MethodPost && r.Body != nil {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		if len(strings.TrimSpace(string(body))) > 0 {
			var in struct {
				PeriodKey string `json:"period_key"`
			}
			if err := json.Unmarshal(body, &in); err != nil {
				writeErr(w, http.StatusBadRequest, "invalid json")
				return
			}
			if strings.TrimSpace(in.PeriodKey) != "" {
				requestedPeriod = strings.TrimSpace(in.PeriodKey)
			}
		}
	}
	period, err := campaignPeriod(c, requestedPeriod, time.Now())
	if err != nil {
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
	rows, err := h.loadCampaignRankRows(r.Context(), c, period, topN)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "load ranking failed: "+err.Error())
		return
	}
	existing, _ := h.store.CampaignAwardMapForPeriod(r.Context(), c.ID, period.Key)
	payable, skipped, spentPlan, details := planCampaignAwards(c, rows, rules, existing)
	warning := ""
	if len(rows) == 0 {
		warning = "ranking is empty"
	} else if payable <= 0 {
		warning = "no payable awards"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"campaign_id": c.ID, "board": c.Board, "top_n": topN,
		"frequency": c.Frequency, "period_key": period.Key,
		"period_start": period.StartDate, "period_end": period.EndDate,
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
	c, err := h.store.GetRankCampaign(r.Context(), id)
	if err != nil || c == nil {
		writeErr(w, http.StatusNotFound, "campaign not found")
		return
	}
	if err := validateCampaignMeta(c, true); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	requestedPeriod := strings.TrimSpace(r.URL.Query().Get("period_key"))
	if len(strings.TrimSpace(string(body))) > 0 {
		var in struct {
			PeriodKey string `json:"period_key"`
		}
		if err := json.Unmarshal(body, &in); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		if strings.TrimSpace(in.PeriodKey) != "" {
			requestedPeriod = strings.TrimSpace(in.PeriodKey)
		}
	}
	period, err := campaignPeriod(c, requestedPeriod, time.Now())
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := h.settleCampaignPeriod(r.Context(), c, period, "manual")
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "load ranking") || strings.Contains(err.Error(), "credit service") {
			status = http.StatusBadGateway
		}
		writeErr(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) settleCampaignPeriod(ctx context.Context, c *store.RankCampaign, period store.CampaignPeriod, trigger string) (map[string]any, error) {
	if h.credit == nil {
		return nil, fmt.Errorf("credit service unavailable")
	}
	h.syncAdminCred()
	if h.effectiveAdminCred() == "" {
		return nil, fmt.Errorf("admin api key required")
	}
	periodRow, err := h.store.EnsureCampaignPeriod(ctx, store.RankCampaignPeriod{
		CampaignID: c.ID, PeriodKey: period.Key, StartDate: period.StartDate, EndDate: period.EndDate,
		Status: store.CampaignPeriodPending,
	})
	if err != nil {
		return nil, err
	}
	if periodRow != nil {
		switch periodRow.Status {
		case store.CampaignPeriodSettled:
			return nil, fmt.Errorf("period %s already settled", period.Key)
		case store.CampaignPeriodEmpty:
			if trigger == "schedule" {
				return nil, fmt.Errorf("period %s has no payable awards", period.Key)
			}
		case store.CampaignPeriodRunning:
			if !periodRow.UpdatedAt.IsZero() && time.Since(periodRow.UpdatedAt) < 30*time.Minute {
				return nil, fmt.Errorf("period %s is already settling", period.Key)
			}
		}
	}
	if err := h.store.MarkCampaignPeriodStatus(ctx, c.ID, period.Key, store.CampaignPeriodRunning, "", false); err != nil {
		return nil, err
	}
	markFailure := func(status, message string) {
		_ = h.store.MarkCampaignPeriodStatus(context.Background(), c.ID, period.Key, status, message, false)
	}
	rules, err := c.ParseRewards()
	if err != nil {
		markFailure(store.CampaignPeriodFailed, "invalid rewards json")
		return nil, fmt.Errorf("invalid rewards json")
	}
	topN := c.TopN
	if topN <= 0 {
		topN = 10
	}
	rows, err := h.loadCampaignRankRows(ctx, c, period, topN)
	if err != nil {
		markFailure(store.CampaignPeriodFailed, "load ranking failed: "+err.Error())
		return nil, fmt.Errorf("load ranking failed: %w", err)
	}
	existing, err := h.store.CampaignAwardMapForPeriod(ctx, c.ID, period.Key)
	if err != nil {
		markFailure(store.CampaignPeriodFailed, err.Error())
		return nil, err
	}
	payable, _, _, _ := planCampaignAwards(c, rows, rules, existing)
	if err := validateCampaignSettleReady(rows, payable); err != nil {
		status := store.CampaignPeriodEmpty
		if len(rows) > 0 {
			status = store.CampaignPeriodEmpty
		}
		markFailure(status, err.Error())
		return nil, err
	}

	spent := 0.0
	for _, a := range existing {
		if a.Status == "success" {
			spent += a.Amount
		}
	}
	awarded, skipped, failed := 0, 0, 0
	details := make([]map[string]any, 0, len(rows))
	for i, row := range rows {
		rank := i + 1
		amt := store.AmountForRank(rules, rank)
		if amt <= 0 {
			skipped++
			details = append(details, map[string]any{"user_id": row.UserID, "rank": rank, "status": "no_reward", "amount": 0, "period_key": period.Key})
			continue
		}
		if prev, ok := existing[row.UserID]; ok && (prev.Status == "success" || prev.Status == "skipped") {
			skipped++
			details = append(details, map[string]any{
				"user_id": row.UserID, "rank": rank, "amount": prev.Amount,
				"status": "already_" + prev.Status, "ledger_id": prev.LedgerID, "period_key": period.Key,
			})
			continue
		}
		if c.BudgetCap > 0 && spent+amt > c.BudgetCap {
			skipped++
			details = append(details, map[string]any{"user_id": row.UserID, "rank": rank, "status": "budget_cut", "amount": amt, "period_key": period.Key})
			continue
		}

		res, grantErr := h.credit.Grant(ctx, credit.Request{
			UserID: row.UserID, Amount: amt, Source: credit.SourceRankReward,
			SourceRef: fmt.Sprintf("campaign:%d:period:%s:rank:%d", c.ID, period.Key, rank),
			Scope:     sub2api.IdempotencyScopeRankReward,
			Slot:      fmt.Sprintf("c%d-p%s-r%d-u%d", c.ID, period.Key, rank, row.UserID),
			Notes:     fmt.Sprintf("rank-campaign:%s:%s:#%d", c.Name, period.Key, rank),
		})
		status := "success"
		ledgerID := int64(0)
		if grantErr != nil {
			failed++
			status = "failed"
			details = append(details, map[string]any{"user_id": row.UserID, "rank": rank, "amount": amt, "status": status, "error": grantErr.Error(), "period_key": period.Key})
			if prev, ok := existing[row.UserID]; ok {
				_ = h.store.UpdateCampaignAwardResult(ctx, prev.ID, amt, 0, status, grantErr.Error())
			} else {
				_, _ = h.store.InsertCampaignAward(ctx, store.RankCampaignAward{CampaignID: c.ID, PeriodKey: period.Key, UserID: row.UserID, Rank: rank, Amount: amt, Status: status, Error: grantErr.Error()})
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
			_ = h.store.UpdateCampaignAwardResult(ctx, prev.ID, amt, ledgerID, status, "")
		} else {
			_, _ = h.store.InsertCampaignAward(ctx, store.RankCampaignAward{CampaignID: c.ID, PeriodKey: period.Key, UserID: row.UserID, Rank: rank, Amount: amt, LedgerID: ledgerID, Status: status})
		}
		details = append(details, map[string]any{"user_id": row.UserID, "rank": rank, "amount": amt, "status": status, "ledger_id": ledgerID, "period_key": period.Key})
	}

	periodStatus := store.CampaignPeriodSettled
	if failed > 0 {
		periodStatus = store.CampaignPeriodPartial
	}
	if err := h.store.MarkCampaignPeriodStatus(ctx, c.ID, period.Key, periodStatus, "", periodStatus == store.CampaignPeriodSettled); err != nil {
		return nil, err
	}
	if store.NormalizeCampaignFrequency(c.Frequency) == store.CampaignFrequencyOnce {
		if failed > 0 {
			_ = h.store.MarkCampaignStatus(ctx, c.ID, store.CampaignStatusPartial, false)
		} else {
			_ = h.store.MarkCampaignSettled(ctx, c.ID)
		}
	}
	return map[string]any{
		"campaign_id": c.ID, "status": periodStatus, "campaign_status": c.Status,
		"period_key": period.Key, "period_start": period.StartDate, "period_end": period.EndDate,
		"awarded": awarded, "skipped": skipped, "failed": failed,
		"spent": spent, "details": details,
	}, nil
}

// PublicRankCampaigns GET /api/ranking/campaigns
func (h *Handler) PublicRankCampaigns(w http.ResponseWriter, r *http.Request) {
	if !h.requireModuleEnabled(w, r, "ranking") {
		return
	}
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	loc, _ := time.LoadLocation(store.CampaignTimezone)
	today := time.Now().In(loc).Format("2006-01-02")
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
