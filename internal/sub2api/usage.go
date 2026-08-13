package sub2api

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const tokenRankingMaxCandidates = 5000

// UsageRankItem is one normalized row from an upstream usage leaderboard.
type UsageRankItem struct {
	UserID               int64   `json:"user_id"`
	DisplayName          string  `json:"display_name"`
	Amount               float64 `json:"amount"`
	RequestCount         int64   `json:"request_count"`
	TokenCount           float64 `json:"token_count"`
	TokenShare           float64 `json:"token_share,omitempty"`
	AvgTokensPerRequest  float64 `json:"avg_tokens_per_request,omitempty"`
	CostPerRequest       float64 `json:"cost_per_request,omitempty"`
	CostPerMillionTokens float64 `json:"cost_per_million_tokens,omitempty"`
	Rank                 int     `json:"rank,omitempty"`
}

// UsageRankResult is the normalized usage ranking payload.
type UsageRankResult struct {
	Items          []UsageRankItem `json:"items"`
	TotalAmount    float64         `json:"total_amount"`
	TotalRequests  int64           `json:"total_requests"`
	TotalTokens    float64         `json:"total_tokens"`
	UserCount      int             `json:"user_count"`
	TopTokens      float64         `json:"top_tokens"`
	Top3TokenShare float64         `json:"top3_token_share"`
	MyRank         int             `json:"my_rank,omitempty"`
	MyAmount       float64         `json:"my_amount,omitempty"`
	MyTokens       float64         `json:"my_tokens,omitempty"`
	MyRequestCount int64           `json:"my_request_count,omitempty"`
	Source         string          `json:"source,omitempty"`
	RawHint        string          `json:"-"`
}

// UsageRankQuery describes a date-bounded ranking request.
type UsageRankQuery struct {
	FromDate string
	ToDate   string
	Period   string // today|yesterday|7d|30d (optional hint for upstream)
	Limit    int
	UserID   int64
}

// FetchUsageRanking tries user-facing ranking first, then admin dashboard ranking.
func (c *Client) FetchUsageRanking(ctx context.Context, userToken string, meta ClientMeta, q UsageRankQuery) (*UsageRankResult, error) {
	if q.Limit <= 0 {
		q.Limit = 20
	}
	if q.Limit > 100 {
		q.Limit = 100
	}
	var lastErr error

	if strings.TrimSpace(userToken) != "" {
		if res, err := c.fetchUserUsageRanking(ctx, userToken, meta, q); err == nil && res != nil {
			res.Source = "user"
			return res, nil
		} else if err != nil {
			lastErr = err
		}
	}

	if c.AdminToken() != "" {
		if res, err := c.fetchAdminUsageRanking(ctx, q); err == nil && res != nil {
			res.Source = "admin"
			// fill my rank from full-ish list when possible
			if q.UserID > 0 && res.MyRank == 0 {
				for _, it := range res.Items {
					if it.UserID == q.UserID {
						res.MyRank = it.Rank
						res.MyAmount = it.Amount
						break
					}
				}
			}
			return res, nil
		} else if err != nil {
			lastErr = err
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("usage ranking unavailable")
	}
	return nil, lastErr
}

// FetchTokenUsageRanking loads the complete admin usage board and ranks it by
// token volume. The upstream endpoint ranks by cost, so truncating before this
// local sort would produce an incorrect token leaderboard.
func (c *Client) FetchTokenUsageRanking(ctx context.Context, q UsageRankQuery) (*UsageRankResult, error) {
	if c.AdminToken() == "" {
		return nil, fmt.Errorf("admin credential required for token ranking")
	}
	displayLimit := q.Limit
	if displayLimit <= 0 {
		displayLimit = 20
	}
	if displayLimit > 100 {
		displayLimit = 100
	}

	fullQuery := q
	fullQuery.Limit = tokenRankingMaxCandidates
	res, err := c.fetchAdminUsageRanking(ctx, fullQuery)
	if err != nil {
		return nil, err
	}
	if res == nil {
		return nil, fmt.Errorf("token ranking unavailable")
	}
	if len(res.Items) >= tokenRankingMaxCandidates {
		return nil, fmt.Errorf("token ranking reached %d-user safety limit", tokenRankingMaxCandidates)
	}

	upstreamTotalTokens := res.TotalTokens
	listedTokens := 0.0
	for _, item := range res.Items {
		listedTokens += item.TokenCount
	}
	if upstreamTotalTokens <= 0 {
		upstreamTotalTokens = listedTokens
	} else {
		tolerance := math.Max(1, math.Abs(upstreamTotalTokens)*1e-9)
		if listedTokens+tolerance < upstreamTotalTokens {
			return nil, fmt.Errorf("token ranking is incomplete: listed %.0f of %.0f tokens", listedTokens, upstreamTotalTokens)
		}
	}

	ranked := res.Items[:0]
	for _, item := range res.Items {
		if item.UserID > 0 && item.TokenCount > 0 {
			ranked = append(ranked, item)
		}
	}
	res.Items = ranked

	sort.SliceStable(res.Items, func(i, j int) bool {
		left, right := res.Items[i], res.Items[j]
		if left.TokenCount != right.TokenCount {
			return left.TokenCount > right.TokenCount
		}
		if left.Amount != right.Amount {
			return left.Amount > right.Amount
		}
		return left.UserID < right.UserID
	})

	res.TotalAmount = 0
	res.TotalRequests = 0
	res.TotalTokens = 0
	for _, item := range res.Items {
		res.TotalAmount += item.Amount
		res.TotalRequests += item.RequestCount
		res.TotalTokens += item.TokenCount
	}

	top3Tokens := 0.0
	res.MyRank = 0
	res.MyAmount = 0
	res.MyTokens = 0
	res.MyRequestCount = 0
	for i := range res.Items {
		item := &res.Items[i]
		item.Rank = i + 1
		if res.TotalTokens > 0 {
			item.TokenShare = item.TokenCount / res.TotalTokens
		}
		if item.RequestCount > 0 {
			item.AvgTokensPerRequest = item.TokenCount / float64(item.RequestCount)
			item.CostPerRequest = item.Amount / float64(item.RequestCount)
		}
		if item.TokenCount > 0 {
			item.CostPerMillionTokens = item.Amount * 1_000_000 / item.TokenCount
		}
		if i < 3 {
			top3Tokens += item.TokenCount
		}
		if q.UserID > 0 && item.UserID == q.UserID {
			res.MyRank = item.Rank
			res.MyAmount = item.Amount
			res.MyTokens = item.TokenCount
			res.MyRequestCount = item.RequestCount
		}
	}

	res.UserCount = len(res.Items)
	if len(res.Items) > 0 {
		res.TopTokens = res.Items[0].TokenCount
	}
	if res.TotalTokens > 0 {
		res.Top3TokenShare = top3Tokens / res.TotalTokens
	}
	if len(res.Items) > displayLimit {
		res.Items = res.Items[:displayLimit]
	}
	res.Source = "admin-token"
	return res, nil
}

func (c *Client) fetchUserUsageRanking(ctx context.Context, userToken string, meta ClientMeta, q UsageRankQuery) (*UsageRankResult, error) {
	paths := []string{
		"/api/v1/usage/ranking",
		"/api/v1/usage/rank",
		"/api/v1/usage/leaderboard",
	}
	paramSets := []url.Values{}
	if q.Period != "" {
		p := url.Values{}
		p.Set("period", q.Period)
		p.Set("limit", strconv.Itoa(q.Limit))
		paramSets = append(paramSets, p)
		p2 := url.Values{}
		p2.Set("range", q.Period)
		p2.Set("limit", strconv.Itoa(q.Limit))
		paramSets = append(paramSets, p2)
	}
	p3 := url.Values{}
	p3.Set("start_date", q.FromDate)
	p3.Set("end_date", q.ToDate)
	p3.Set("limit", strconv.Itoa(q.Limit))
	paramSets = append(paramSets, p3)

	var lastErr error
	for _, path := range paths {
		for _, params := range paramSets {
			body, status, err := c.doUserGET(ctx, path, params, userToken, meta)
			if err != nil {
				lastErr = err
				continue
			}
			if status >= 300 {
				lastErr = fmt.Errorf("user ranking %s status=%d body=%s", path, status, truncate(body, 200))
				continue
			}
			res, err := parseUsageRankBody(body, q.Limit)
			if err != nil {
				lastErr = err
				continue
			}
			return res, nil
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("user ranking endpoints failed")
	}
	return nil, lastErr
}

func (c *Client) fetchAdminUsageRanking(ctx context.Context, q UsageRankQuery) (*UsageRankResult, error) {
	params := url.Values{}
	params.Set("start_date", q.FromDate)
	params.Set("end_date", q.ToDate)
	params.Set("limit", strconv.Itoa(q.Limit))
	// Some builds accept larger limits for computing my_rank; request a bit more.
	if q.UserID > 0 && q.Limit < 100 {
		params.Set("limit", strconv.Itoa(100))
	}

	path := "/api/v1/admin/dashboard/users-ranking"
	reqURL := c.baseURL + path + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	c.applyAdminAuth(req)
	req.Header.Set("Accept", "application/json")
	c.applyInternalHost(req)
	body, status, err := c.do(req)
	if err != nil {
		return nil, err
	}
	if status >= 300 {
		return nil, fmt.Errorf("admin ranking failed: status=%d body=%s", status, truncate(body, 240))
	}
	res, err := parseUsageRankBody(body, q.Limit)
	if err != nil {
		return nil, err
	}
	// Truncate display list to requested limit after parsing.
	if len(res.Items) > q.Limit {
		res.Items = res.Items[:q.Limit]
	}
	return res, nil
}

func (c *Client) doUserGET(ctx context.Context, path string, params url.Values, userToken string, meta ClientMeta) ([]byte, int, error) {
	u := c.baseURL + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+normalizeToken(userToken))
	req.Header.Set("Accept", "application/json")
	c.applyInternalHost(req)
	c.applyClientMeta(req, meta)
	return c.do(req)
}

// PublicOrigin returns a browser-facing origin for deep links when possible.
func (c *Client) PublicOrigin() string {
	host := strings.TrimSpace(c.publicHost)
	if host != "" {
		if strings.HasPrefix(host, "http://") || strings.HasPrefix(host, "https://") {
			return strings.TrimRight(host, "/")
		}
		return "https://" + strings.TrimRight(host, "/")
	}
	base := strings.TrimRight(c.baseURL, "/")
	// Avoid advertising docker-internal hostnames.
	if strings.Contains(base, "://sub2api") || strings.Contains(base, ".internal") {
		return ""
	}
	return base
}

func parseUsageRankBody(body []byte, limit int) (*UsageRankResult, error) {
	var raw any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode ranking json: %w", err)
	}
	// unwrap data envelope
	if m, ok := raw.(map[string]any); ok {
		if d, ok := m["data"]; ok && d != nil {
			raw = d
		}
	}

	res := &UsageRankResult{}
	switch v := raw.(type) {
	case map[string]any:
		res.TotalAmount = firstFloat(v, "total_actual_cost", "total_amount", "total_cost", "total_spending", "sum_amount")
		res.TotalRequests = int64(firstFloat(v, "total_requests", "request_count", "requests"))
		res.TotalTokens = firstFloat(v, "total_tokens", "token_count", "tokens")
		res.MyRank = int(firstFloat(v, "my_rank", "myRank", "rank"))
		res.MyAmount = firstFloat(v, "my_amount", "myAmount", "my_actual_cost", "my_cost")
		list := firstArray(v, "ranking", "items", "list", "users", "rows", "data")
		res.Items = mapUsageItems(list, limit)
		if res.TotalAmount == 0 {
			for _, it := range res.Items {
				res.TotalAmount += it.Amount
			}
		}
		if res.TotalRequests == 0 {
			for _, it := range res.Items {
				res.TotalRequests += it.RequestCount
			}
		}
		if res.TotalTokens == 0 {
			for _, it := range res.Items {
				res.TotalTokens += it.TokenCount
			}
		}
	case []any:
		res.Items = mapUsageItems(v, limit)
		for _, it := range res.Items {
			res.TotalAmount += it.Amount
			res.TotalRequests += it.RequestCount
			res.TotalTokens += it.TokenCount
		}
	default:
		return nil, fmt.Errorf("unexpected ranking payload type %T", raw)
	}
	if len(res.Items) == 0 {
		// empty board is still valid
		return res, nil
	}
	return res, nil
}

func mapUsageItems(list []any, limit int) []UsageRankItem {
	out := make([]UsageRankItem, 0, len(list))
	for i, row := range list {
		m, ok := row.(map[string]any)
		if !ok {
			continue
		}
		// nested user object
		if u, ok := m["user"].(map[string]any); ok {
			for k, v := range u {
				if _, exists := m[k]; !exists {
					m[k] = v
				}
			}
		}
		it := UsageRankItem{
			UserID:       int64(firstFloat(m, "user_id", "userId", "id", "uid")),
			DisplayName:  firstMapString(m, "display_name", "displayName", "username", "name", "email", "user_name", "masked_name"),
			Amount:       firstFloat(m, "actual_cost", "total_actual_cost", "amount", "cost", "spending", "total_cost", "consume"),
			RequestCount: int64(firstFloat(m, "request_count", "requests", "total_requests", "request")),
			TokenCount:   firstFloat(m, "total_tokens", "token_count", "tokens", "token"),
			Rank:         int(firstFloat(m, "rank", "position", "idx")),
		}
		if it.Rank <= 0 {
			it.Rank = i + 1
		}
		if it.DisplayName == "" && it.UserID > 0 {
			it.DisplayName = fmt.Sprintf("用户%d", it.UserID)
		}
		out = append(out, it)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func firstArray(m map[string]any, keys ...string) []any {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if arr, ok := v.([]any); ok {
				return arr
			}
		}
	}
	return nil
}

func firstMapString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			switch t := v.(type) {
			case string:
				if s := strings.TrimSpace(t); s != "" {
					return s
				}
			case float64:
				return strconv.FormatInt(int64(t), 10)
			case json.Number:
				return t.String()
			}
		}
	}
	return ""
}

func firstFloat(m map[string]any, keys ...string) float64 {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			switch t := v.(type) {
			case float64:
				return t
			case float32:
				return float64(t)
			case int:
				return float64(t)
			case int64:
				return float64(t)
			case json.Number:
				f, _ := t.Float64()
				return f
			case string:
				f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
				if err == nil {
					return f
				}
			}
		}
	}
	return 0
}

// ResolveRange converts period labels into inclusive dates in loc.
func ResolveRange(period string, now time.Time, loc *time.Location) (from, to, normalized string) {
	if loc == nil {
		loc = time.Local
	}
	now = now.In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	p := strings.ToLower(strings.TrimSpace(period))
	switch p {
	case "", "today", "今日":
		return today.Format("2006-01-02"), today.Format("2006-01-02"), "today"
	case "yesterday", "昨日":
		d := today.AddDate(0, 0, -1)
		return d.Format("2006-01-02"), d.Format("2006-01-02"), "yesterday"
	case "7d", "last_7", "last7", "近7天", "week":
		from := today.AddDate(0, 0, -6)
		return from.Format("2006-01-02"), today.Format("2006-01-02"), "7d"
	case "30d", "last_30", "last30", "近30天", "month":
		from := today.AddDate(0, 0, -29)
		return from.Format("2006-01-02"), today.Format("2006-01-02"), "30d"
	default:
		// allow explicit YYYY-MM-DD,YYYY-MM-DD
		if parts := strings.Split(p, ","); len(parts) == 2 {
			a := strings.TrimSpace(parts[0])
			b := strings.TrimSpace(parts[1])
			if _, err1 := time.Parse("2006-01-02", a); err1 == nil {
				if _, err2 := time.Parse("2006-01-02", b); err2 == nil {
					return a, b, "custom"
				}
			}
		}
		return today.Format("2006-01-02"), today.Format("2006-01-02"), "today"
	}
}
