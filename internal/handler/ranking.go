package handler

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"sub2api-ext/internal/sub2api"
)

type rankingSummary struct {
	TotalAmount    float64 `json:"total_amount"`
	TotalRequests  int64   `json:"total_requests,omitempty"`
	TotalTokens    float64 `json:"total_tokens,omitempty"`
	DisplayCount   int     `json:"display_count"`
	TopAmount      float64 `json:"top_amount"`
	TopTokens      float64 `json:"top_tokens,omitempty"`
	Top3TokenShare float64 `json:"top3_token_share,omitempty"`
	MyRank         int     `json:"my_rank"`
	MyAmount       float64 `json:"my_amount"`
	MyTokens       float64 `json:"my_tokens,omitempty"`
	MyRequestCount int64   `json:"my_request_count,omitempty"`
	UserCount      int64   `json:"user_count,omitempty"`
}

type rankingItem struct {
	Rank                 int     `json:"rank"`
	UserID               int64   `json:"user_id,omitempty"`
	DisplayName          string  `json:"display_name"`
	Amount               float64 `json:"amount"`
	RequestCount         int64   `json:"request_count,omitempty"`
	TokenCount           float64 `json:"token_count,omitempty"`
	TokenShare           float64 `json:"token_share,omitempty"`
	AvgTokensPerRequest  float64 `json:"avg_tokens_per_request,omitempty"`
	CostPerRequest       float64 `json:"cost_per_request,omitempty"`
	CostPerMillionTokens float64 `json:"cost_per_million_tokens,omitempty"`
	CheckinAmount        float64 `json:"checkin_amount,omitempty"`
	LotteryAmount        float64 `json:"lottery_amount,omitempty"`
	CheckinCount         int64   `json:"checkin_count,omitempty"`
	LotteryCount         int64   `json:"lottery_count,omitempty"`
	IsMe                 bool    `json:"is_me"`
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
	if !h.requireModuleEnabled(w, r, "ranking") {
		return
	}
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
	if !h.requireModuleEnabled(w, r, "ranking") {
		return
	}
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

	res, err := h.client.FetchTokenUsageRanking(r.Context(), q)
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
			Warning:     "Token 使用排行暂不可用：" + err.Error(),
		})
		return
	}

	items := make([]rankingItem, 0, len(res.Items))
	for _, it := range res.Items {
		name := maskDisplayName(it.DisplayName)
		isMe := me != nil && it.UserID == me.ID
		if isMe {
			name = maskDisplayName(firstNonEmptyStr(me.Username, me.Email, it.DisplayName))
		}
		items = append(items, rankingItem{
			Rank:                 it.Rank,
			UserID:               it.UserID,
			DisplayName:          name,
			Amount:               it.Amount,
			RequestCount:         it.RequestCount,
			TokenCount:           it.TokenCount,
			TokenShare:           it.TokenShare,
			AvgTokensPerRequest:  it.AvgTokensPerRequest,
			CostPerRequest:       it.CostPerRequest,
			CostPerMillionTokens: it.CostPerMillionTokens,
			IsMe:                 isMe,
		})
	}

	topAmount := 0.0
	if len(items) > 0 {
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
			TotalAmount:    res.TotalAmount,
			TotalRequests:  res.TotalRequests,
			TotalTokens:    res.TotalTokens,
			DisplayCount:   len(items),
			TopAmount:      topAmount,
			TopTokens:      res.TopTokens,
			Top3TokenShare: res.Top3TokenShare,
			MyRank:         res.MyRank,
			MyAmount:       res.MyAmount,
			MyTokens:       res.MyTokens,
			MyRequestCount: res.MyRequestCount,
			UserCount:      int64(res.UserCount),
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

type nameCacheEntry struct {
	name string
	exp  time.Time
}

const (
	rankNameCacheTTL = 10 * time.Minute
	rankNameWorkers  = 6
)

// resolveRankDisplayNames fills masked names for ranking rows.
// Prefer current user profile for self; otherwise admin GetUserByAdmin when available.
// Uses a short TTL cache and limited concurrency to avoid N serial admin calls.
func (h *Handler) resolveRankDisplayNames(ctx context.Context, userIDs []int64, me *sub2api.User) map[int64]string {
	out := make(map[int64]string, len(userIDs))
	if h == nil {
		return out
	}
	h.syncAdminCred()
	now := time.Now()

	// unique positive ids, preserve first-seen order
	needFetch := make([]int64, 0, len(userIDs))
	seen := make(map[int64]struct{}, len(userIDs))
	for _, id := range userIDs {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}

		if me != nil && me.ID == id {
			raw := firstNonEmptyStr(me.Username, me.Email, fmt.Sprintf("用户%d", id))
			out[id] = maskDisplayName(raw)
			h.putRankNameCache(id, raw, now)
			continue
		}
		if raw, ok := h.getRankNameCache(id, now); ok {
			out[id] = maskDisplayName(raw)
			continue
		}
		needFetch = append(needFetch, id)
	}

	if len(needFetch) == 0 {
		return out
	}

	canAdmin := false
	if h.client != nil {
		if h.settings != nil && h.effectiveAdminCred() != "" {
			canAdmin = true
		} else if h.client.AdminToken() != "" {
			canAdmin = true
		}
	}
	if !canAdmin {
		for _, id := range needFetch {
			raw := fmt.Sprintf("用户%d", id)
			out[id] = maskDisplayName(raw)
			// do not cache bare fallbacks long-term? still cache briefly to avoid stampede
			h.putRankNameCache(id, raw, now)
		}
		return out
	}

	type fetched struct {
		id  int64
		raw string
	}
	jobs := make(chan int64, len(needFetch))
	results := make(chan fetched, len(needFetch))
	workers := rankNameWorkers
	if workers > len(needFetch) {
		workers = len(needFetch)
	}
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range jobs {
				raw := fmt.Sprintf("用户%d", id)
				if u, err := h.client.GetUserByAdmin(ctx, id); err == nil && u != nil {
					if v := firstNonEmptyStr(u.Username, u.Email); v != "" {
						raw = v
					}
				}
				results <- fetched{id: id, raw: raw}
			}
		}()
	}
	for _, id := range needFetch {
		jobs <- id
	}
	close(jobs)
	wg.Wait()
	close(results)

	for item := range results {
		out[item.id] = maskDisplayName(item.raw)
		h.putRankNameCache(item.id, item.raw, now)
	}
	// ensure every requested id has an entry even if context cancelled mid-flight
	for _, id := range needFetch {
		if _, ok := out[id]; ok {
			continue
		}
		raw := fmt.Sprintf("用户%d", id)
		out[id] = maskDisplayName(raw)
	}
	return out
}

func (h *Handler) getRankNameCache(id int64, now time.Time) (string, bool) {
	h.nameCacheMu.Lock()
	defer h.nameCacheMu.Unlock()
	if h.nameCache == nil {
		return "", false
	}
	ent, ok := h.nameCache[id]
	if !ok || now.After(ent.exp) {
		if ok {
			delete(h.nameCache, id)
		}
		return "", false
	}
	return ent.name, true
}

func (h *Handler) putRankNameCache(id int64, raw string, now time.Time) {
	if id <= 0 || strings.TrimSpace(raw) == "" {
		return
	}
	h.nameCacheMu.Lock()
	defer h.nameCacheMu.Unlock()
	if h.nameCache == nil {
		h.nameCache = make(map[int64]nameCacheEntry)
	}
	h.nameCache[id] = nameCacheEntry{name: raw, exp: now.Add(rankNameCacheTTL)}
	// soft bound to avoid unbounded growth
	if len(h.nameCache) > 2000 {
		for k, v := range h.nameCache {
			if now.After(v.exp) {
				delete(h.nameCache, k)
			}
		}
	}
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
