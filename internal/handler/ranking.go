package handler

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"sub2api-ext/internal/sub2api"
)

type rankingSummary struct {
	TotalAmount  float64 `json:"total_amount"`
	DisplayCount int     `json:"display_count"`
	TopAmount    float64 `json:"top_amount"`
	MyRank       int     `json:"my_rank"`
	MyAmount     float64 `json:"my_amount"`
	UserCount    int64   `json:"user_count,omitempty"`
}

type rankingItem struct {
	Rank          int     `json:"rank"`
	UserID        int64   `json:"user_id,omitempty"`
	DisplayName   string  `json:"display_name"`
	Amount        float64 `json:"amount"`
	RequestCount  int64   `json:"request_count,omitempty"`
	TokenCount    float64 `json:"token_count,omitempty"`
	CheckinAmount float64 `json:"checkin_amount,omitempty"`
	LotteryAmount float64 `json:"lottery_amount,omitempty"`
	CheckinCount  int64   `json:"checkin_count,omitempty"`
	LotteryCount  int64   `json:"lottery_count,omitempty"`
	IsMe          bool    `json:"is_me"`
}

type rankingResponse struct {
	Board       string         `json:"board"`
	Range       string         `json:"range"`
	From        string         `json:"from"`
	To          string         `json:"to"`
	Limit       int            `json:"limit"`
	Timezone    string         `json:"timezone"`
	Summary     rankingSummary `json:"summary"`
	Items       []rankingItem  `json:"items"`
	Source      string         `json:"source,omitempty"`
	FallbackURL string         `json:"fallback_url,omitempty"`
	Warning     string         `json:"warning,omitempty"`
}

// RankingRewards GET /api/ranking/rewards
func (h *Handler) RankingRewards(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.limitStatus.Allow("ranking-rewards:" + clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "rate limited")
		return
	}

	period := r.URL.Query().Get("range")
	if period == "" {
		period = r.URL.Query().Get("period")
	}
	limit := parseRankLimit(r.URL.Query().Get("limit"))
	from, to, normalized := sub2api.ResolveRange(period, time.Now(), h.settings.Location())

	var me *sub2api.User
	if token := extractToken(r); token != "" {
		if u, err := h.client.ResolveUser(r.Context(), token, clientMetaFromRequest(r)); err == nil {
			me = u
		}
	}

	rows, summary, err := h.store.ListRewardRanking(r.Context(), from, to, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load rewards ranking failed: "+err.Error())
		return
	}

	myRank, myAmount := 0, 0.0
	if me != nil {
		myRank, myAmount, _ = h.store.RewardRankOfUser(r.Context(), from, to, me.ID)
	}

	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.UserID)
	}
	names := h.resolveRankDisplayNames(r.Context(), ids, me)

	items := make([]rankingItem, 0, len(rows))
	for i, row := range rows {
		name := names[row.UserID]
		if name == "" {
			name = maskDisplayName(fmt.Sprintf("用户%d", row.UserID))
		}
		items = append(items, rankingItem{
			Rank:          i + 1,
			UserID:        row.UserID,
			DisplayName:   name,
			Amount:        row.TotalAmount,
			CheckinAmount: row.CheckinAmount,
			LotteryAmount: row.LotteryAmount,
			CheckinCount:  row.CheckinCount,
			LotteryCount:  row.LotteryCount,
			IsMe:          me != nil && me.ID == row.UserID,
		})
	}

	topAmount := summary.TopAmount
	if topAmount == 0 && len(items) > 0 {
		topAmount = items[0].Amount
	}

	writeJSON(w, http.StatusOK, rankingResponse{
		Board:    "rewards",
		Range:    normalized,
		From:     from,
		To:       to,
		Limit:    limit,
		Timezone: h.settings.Get().Timezone,
		Summary: rankingSummary{
			TotalAmount:  summary.TotalAmount,
			DisplayCount: len(items),
			TopAmount:    topAmount,
			MyRank:       myRank,
			MyAmount:     myAmount,
			UserCount:    summary.UserCount,
		},
		Items:  items,
		Source: "local",
	})
}

// RankingConsumption GET /api/ranking/consumption
func (h *Handler) RankingConsumption(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.limitStatus.Allow("ranking-consumption:" + clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "rate limited")
		return
	}

	period := r.URL.Query().Get("range")
	if period == "" {
		period = r.URL.Query().Get("period")
	}
	limit := parseRankLimit(r.URL.Query().Get("limit"))
	from, to, normalized := sub2api.ResolveRange(period, time.Now(), h.settings.Location())

	token := extractToken(r)
	var me *sub2api.User
	if token != "" {
		if u, err := h.client.ResolveUser(r.Context(), token, clientMetaFromRequest(r)); err == nil {
			me = u
		}
	}

	fallback := h.officialRankURL()
	q := sub2api.UsageRankQuery{
		FromDate: from,
		ToDate:   to,
		Period:   normalized,
		Limit:    limit,
	}
	if me != nil {
		q.UserID = me.ID
	}

	h.syncAdminCred()

	res, err := h.client.FetchUsageRanking(r.Context(), token, clientMetaFromRequest(r), q)
	if err != nil {
		writeJSON(w, http.StatusOK, rankingResponse{
			Board:       "consumption",
			Range:       normalized,
			From:        from,
			To:          to,
			Limit:       limit,
			Timezone:    h.settings.Get().Timezone,
			Summary:     rankingSummary{DisplayCount: 0},
			Items:       []rankingItem{},
			FallbackURL: fallback,
			Warning:     "官方消费排行暂不可用：" + err.Error(),
		})
		return
	}

	items := make([]rankingItem, 0, len(res.Items))
	topAmount := 0.0
	for _, it := range res.Items {
		name := maskDisplayName(it.DisplayName)
		isMe := me != nil && it.UserID == me.ID
		if isMe {
			name = maskDisplayName(firstNonEmptyStr(me.Username, me.Email, it.DisplayName))
		}
		if it.Amount > topAmount {
			topAmount = it.Amount
		}
		items = append(items, rankingItem{
			Rank:         it.Rank,
			UserID:       it.UserID,
			DisplayName:  name,
			Amount:       it.Amount,
			RequestCount: it.RequestCount,
			TokenCount:   it.TokenCount,
			IsMe:         isMe,
		})
	}

	myRank, myAmount := res.MyRank, res.MyAmount
	if me != nil && myRank == 0 {
		for _, it := range res.Items {
			if it.UserID == me.ID {
				myRank = it.Rank
				myAmount = it.Amount
				break
			}
		}
	}
	if topAmount == 0 && len(items) > 0 {
		topAmount = items[0].Amount
	}

	writeJSON(w, http.StatusOK, rankingResponse{
		Board:    "consumption",
		Range:    normalized,
		From:     from,
		To:       to,
		Limit:    limit,
		Timezone: h.settings.Get().Timezone,
		Summary: rankingSummary{
			TotalAmount:  res.TotalAmount,
			DisplayCount: len(items),
			TopAmount:    topAmount,
			MyRank:       myRank,
			MyAmount:     myAmount,
		},
		Items:       items,
		Source:      res.Source,
		FallbackURL: fallback,
	})
}

func parseRankLimit(raw string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(raw))
	if n <= 0 {
		return 20
	}
	if n > 100 {
		return 100
	}
	return n
}

func (h *Handler) officialRankURL() string {
	origin := strings.TrimRight(h.client.PublicOrigin(), "/")
	if origin == "" {
		return "/rank"
	}
	return origin + "/rank"
}


// resolveRankDisplayNames fills masked names for ranking rows.
// Prefer current user profile for self; otherwise admin GetUserByAdmin when available.
func (h *Handler) resolveRankDisplayNames(ctx context.Context, userIDs []int64, me *sub2api.User) map[int64]string {
	out := make(map[int64]string, len(userIDs))
	h.syncAdminCred()
	for _, id := range userIDs {
		if id <= 0 {
			continue
		}
		if _, ok := out[id]; ok {
			continue
		}
		if me != nil && me.ID == id {
			out[id] = maskDisplayName(firstNonEmptyStr(me.Username, me.Email, fmt.Sprintf("用户%d", id)))
			continue
		}
		name := fmt.Sprintf("用户%d", id)
		if h.client != nil && h.effectiveAdminCred() != "" {
			if u, err := h.client.GetUserByAdmin(ctx, id); err == nil && u != nil {
				if v := firstNonEmptyStr(u.Username, u.Email); v != "" {
					name = v
				}
			}
		}
		out[id] = maskDisplayName(name)
	}
	return out
}

func maskDisplayName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "***"
	}
	if strings.Contains(name, "***") {
		return name
	}
	runes := []rune(name)
	n := utf8.RuneCountInString(name)
	if n <= 2 {
		return "***"
	}
	if n <= 4 {
		return string(runes[:1]) + "***" + string(runes[n-1:])
	}
	return string(runes[:2]) + "***" + string(runes[n-2:])
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
