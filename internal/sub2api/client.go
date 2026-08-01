package sub2api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Client struct {
	mu         sync.RWMutex
	baseURL    string
	adminToken string
	publicHost string
	httpClient *http.Client
}

type User struct {
	ID             int64      `json:"id"`
	Email          string     `json:"email"`
	Username       string     `json:"username"`
	Balance        float64    `json:"balance"`
	FrozenBalance  float64    `json:"frozen_balance"`
	Role           string     `json:"role"`
	Status         string     `json:"status"`
	LastActiveAt   *time.Time `json:"last_active_at"`
	LastUsedAt     *time.Time `json:"last_used_at"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	TotalRecharged float64    `json:"total_recharged"`
}

type UserPage struct {
	Items    []User `json:"items"`
	Total    int    `json:"total"`
	Page     int    `json:"page"`
	PageSize int    `json:"page_size"`
	Pages    int    `json:"pages"`
}

type UserUsageStat struct {
	UserID          int64   `json:"user_id"`
	TodayActualCost float64 `json:"today_actual_cost"`
	TotalActualCost float64 `json:"total_actual_cost"`
}

type BatchUserUsageResult struct {
	Stats map[string]UserUsageStat `json:"stats"`
}

// ClientMeta 透传浏览器侧网络指纹，避免 SESSION_BINDING_MISMATCH
type ClientMeta struct {
	ClientIP     string
	UserAgent    string
	AcceptLang   string
	ForwardedFor string
	XRealIP      string
	CFConnecting string
}

type apiEnvelope struct {
	Code    any             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
	Detail  string          `json:"detail"`
}

func New(baseURL, adminToken string, timeout time.Duration) *Client {
	return NewWithPublicHost(baseURL, adminToken, "", timeout)
}

// NewWithPublicHost creates a client. publicHost is optional external host header
// when baseURL points to an internal docker service name.
func NewWithPublicHost(baseURL, adminToken, publicHost string, timeout time.Duration) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		adminToken: adminToken,
		publicHost: strings.TrimSpace(publicHost),
		httpClient: &http.Client{Timeout: timeout},
	}
}

// ResolveUser 解析当前用户：
// 1) 用用户 token + 真实客户端指纹调 auth/me
// 2) 若 SESSION_BINDING_MISMATCH，则解析 JWT 取 user_id，再用 Admin 接口查用户

// SetAdminToken hot-updates the server-side admin credential used for Sub2API admin calls.
func (c *Client) SetAdminToken(token string) {
	c.mu.Lock()
	c.adminToken = strings.TrimSpace(token)
	c.mu.Unlock()
}

// AdminToken returns the current admin credential (may be empty).
func (c *Client) AdminToken() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return strings.TrimSpace(c.adminToken)
}

func (c *Client) ResolveUser(ctx context.Context, userToken string, meta ClientMeta) (*User, error) {
	userToken = normalizeToken(userToken)
	if userToken == "" {
		return nil, fmt.Errorf("empty user token")
	}

	user, err := c.fetchUser(ctx, "/api/v1/auth/me", userToken, meta)
	if err == nil {
		return user, nil
	}
	// profile 备用
	if u2, err2 := c.fetchUser(ctx, "/api/v1/user/profile", userToken, meta); err2 == nil {
		return u2, nil
	}

	// 会话指纹不匹配：浏览器能用 token，容器回放不行 → JWT 解析 + Admin 查询
	if strings.Contains(err.Error(), "SESSION_BINDING_MISMATCH") ||
		strings.Contains(err.Error(), "fingerprint") ||
		strings.Contains(err.Error(), "SESSION_BINDING") {
		claims, perr := parseJWTClaims(userToken)
		if perr != nil {
			return nil, fmt.Errorf("%v; jwt parse failed: %v", err, perr)
		}
		uid := claims.UserID
		if uid <= 0 {
			return nil, fmt.Errorf("%v; user id claim not found in jwt", err)
		}
		if c.AdminToken() != "" {
			adminUser, aerr := c.GetUserByAdmin(ctx, uid)
			if aerr == nil {
				return adminUser, nil
			}
			return &User{ID: uid, Role: claims.Role, Email: claims.Email, Username: claims.Username}, nil
		}
		return &User{ID: uid, Role: claims.Role, Email: claims.Email, Username: claims.Username}, nil
	}
	return nil, err
}

type jwtClaims struct {
	UserID   int64
	Role     string
	Email    string
	Username string
}

func (c *Client) GetUserByAdmin(ctx context.Context, userID int64) (*User, error) {
	if c.AdminToken() == "" {
		return nil, fmt.Errorf("admin credential empty")
	}
	url := fmt.Sprintf("%s/api/v1/admin/users/%d", c.baseURL, userID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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
		return nil, fmt.Errorf("admin get user failed: status=%d body=%s", status, truncate(body, 300))
	}
	var user User
	if err := decodeData(body, &user); err != nil || user.ID == 0 {
		if err2 := json.Unmarshal(body, &user); err2 != nil || user.ID == 0 {
			return nil, fmt.Errorf("decode admin user: %v raw=%s", err, truncate(body, 300))
		}
	}
	return &user, nil
}

// ListAdminUsers returns one page from the upstream admin user list.
func (c *Client) ListAdminUsers(ctx context.Context, page, pageSize int) (*UserPage, error) {
	if c.AdminToken() == "" {
		return nil, fmt.Errorf("admin credential empty")
	}
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 100
	}
	params := url.Values{}
	params.Set("page", strconv.Itoa(page))
	params.Set("page_size", strconv.Itoa(pageSize))
	params.Set("include_subscriptions", "false")
	url := c.baseURL + "/api/v1/admin/users?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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
		return nil, fmt.Errorf("admin list users failed: status=%d body=%s", status, truncate(body, 300))
	}
	var out UserPage
	if err := decodeData(body, &out); err != nil {
		return nil, fmt.Errorf("decode admin users: %w", err)
	}
	return &out, nil
}

// ListAllAdminUsers loads every upstream user page with an upper safety bound.
func (c *Client) ListAllAdminUsers(ctx context.Context, maxUsers int) ([]User, error) {
	if maxUsers <= 0 {
		maxUsers = 5000
	}
	const pageSize = 100
	out := make([]User, 0, minInt(maxUsers, pageSize))
	for page := 1; ; page++ {
		res, err := c.ListAdminUsers(ctx, page, pageSize)
		if err != nil {
			return nil, err
		}
		for _, user := range res.Items {
			if len(out) >= maxUsers {
				return out, nil
			}
			out = append(out, user)
		}
		if len(res.Items) == 0 || page >= res.Pages || len(out) >= res.Total {
			return out, nil
		}
	}
}

// BatchUserUsage returns lifetime/today usage totals for the requested users.
func (c *Client) BatchUserUsage(ctx context.Context, userIDs []int64) (map[int64]UserUsageStat, error) {
	out := make(map[int64]UserUsageStat, len(userIDs))
	if len(userIDs) == 0 {
		return out, nil
	}
	if c.AdminToken() == "" {
		return nil, fmt.Errorf("admin credential empty")
	}
	const chunkSize = 100
	for start := 0; start < len(userIDs); start += chunkSize {
		end := start + chunkSize
		if end > len(userIDs) {
			end = len(userIDs)
		}
		raw, err := json.Marshal(map[string]any{"user_ids": userIDs[start:end]})
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/admin/dashboard/users-usage", bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		c.applyAdminAuth(req)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		c.applyInternalHost(req)
		body, status, err := c.do(req)
		if err != nil {
			return nil, err
		}
		if status >= 300 {
			return nil, fmt.Errorf("admin batch user usage failed: status=%d body=%s", status, truncate(body, 300))
		}
		var res BatchUserUsageResult
		if err := decodeData(body, &res); err != nil {
			return nil, fmt.Errorf("decode batch user usage: %w", err)
		}
		for key, stat := range res.Stats {
			id := stat.UserID
			if id <= 0 {
				id, _ = strconv.ParseInt(key, 10, 64)
				stat.UserID = id
			}
			if id > 0 {
				out[id] = stat
			}
		}
	}
	return out, nil
}

func (c *Client) fetchUser(ctx context.Context, path, userToken string, meta ClientMeta) (*User, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+userToken)
	req.Header.Set("Accept", "application/json")
	c.applyInternalHost(req)
	c.applyClientMeta(req, meta)

	body, status, err := c.do(req)
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		code := extractErrorCode(body)
		return nil, fmt.Errorf("invalid or expired user token (path=%s status=%d code=%s body=%s token_len=%d)",
			path, status, code, truncate(body, 160), len(userToken))
	}
	if status >= 300 {
		return nil, fmt.Errorf("%s failed: status=%d body=%s", path, status, truncate(body, 300))
	}

	var user User
	if err := decodeData(body, &user); err != nil {
		if err2 := json.Unmarshal(body, &user); err2 != nil || user.ID == 0 {
			var wrap struct {
				User *User `json:"user"`
			}
			if err3 := decodeData(body, &wrap); err3 == nil && wrap.User != nil && wrap.User.ID != 0 {
				return wrap.User, nil
			}
			return nil, fmt.Errorf("decode user: %v raw=%s", err, truncate(body, 300))
		}
	}
	if user.ID == 0 {
		return nil, fmt.Errorf("user id missing in %s response", path)
	}
	return &user, nil
}

// Ping checks upstream reachability with a cheap request.
func (c *Client) Ping(ctx context.Context) error {
	if c.baseURL == "" {
		return fmt.Errorf("empty base url")
	}
	// Prefer a lightweight path; fall back to root.
	for _, path := range []string{"/api/v1/health", "/healthz", "/health", "/"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
		if err != nil {
			return err
		}
		c.applyInternalHost(req)
		req.Header.Set("Accept", "application/json")
		body, status, err := c.do(req)
		if err != nil {
			return err
		}
		// Any HTTP response means network/path is up enough for readiness.
		if status > 0 {
			_ = body
			return nil
		}
	}
	return fmt.Errorf("no reachable health endpoint")
}

// AddBalance calls admin balance API: POST /api/v1/admin/users/:id/balance
// using the default "checkin" idempotency scope.
func (c *Client) AddBalance(ctx context.Context, userID int64, amount float64, notes, checkinDate string) (*User, error) {
	return c.AddBalanceScoped(ctx, IdempotencyScopeCheckin, userID, amount, notes, checkinDate)
}

// AddBalanceScoped credits a user with an explicit idempotency scope.
//
// The scope keeps concurrent grant sources apart: check-in and lottery may
// credit the same user on the same day, and a shared key would make the
// upstream swallow the second call as a duplicate.
func (c *Client) AddBalanceScoped(ctx context.Context, scope string, userID int64, amount float64, notes, slot string) (*User, error) {
	return c.adjustBalanceScoped(ctx, scope, userID, amount, "add", notes, slot)
}

// SubtractBalanceScoped atomically subtracts balance without allowing a negative result.
func (c *Client) SubtractBalanceScoped(ctx context.Context, scope string, userID int64, amount float64, notes, slot string) (*User, error) {
	return c.adjustBalanceScoped(ctx, scope, userID, amount, "subtract", notes, slot)
}

func (c *Client) adjustBalanceScoped(ctx context.Context, scope string, userID int64, amount float64, operation, notes, slot string) (*User, error) {
	if amount <= 0 {
		return nil, fmt.Errorf("amount must be > 0")
	}
	if operation != "add" && operation != "subtract" {
		return nil, fmt.Errorf("unsupported balance operation: %s", operation)
	}
	if c.AdminToken() == "" {
		return nil, fmt.Errorf("admin credential empty")
	}
	payload := map[string]any{
		"balance":   amount,
		"operation": operation,
		"notes":     notes,
	}
	raw, _ := json.Marshal(payload)
	url := fmt.Sprintf("%s/api/v1/admin/users/%d/balance", c.baseURL, userID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	c.applyAdminAuth(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	// sub2api 要求 Idempotency-Key 为可打印 ASCII 且无空格，长度 <= 128
	// notes 可能含空格，不能直接拼进 key
	req.Header.Set("Idempotency-Key", IdempotencyKey(scope, userID, slot))
	c.applyInternalHost(req)

	body, status, err := c.do(req)
	if err != nil {
		return nil, err
	}
	if status >= 300 {
		if looksLikeIdempotentHit(status, body) {
			// 上游可能已按幂等键入账，回查用户余额
			if u, err := c.GetUserByAdmin(ctx, userID); err == nil {
				return u, nil
			}
		}
		return nil, fmt.Errorf("update balance failed: status=%d body=%s", status, truncate(body, 400))
	}
	var user User
	if err := decodeData(body, &user); err != nil {
		if err2 := json.Unmarshal(body, &user); err2 != nil {
			return nil, fmt.Errorf("decode balance response: %v", err)
		}
	}
	return &user, nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (c *Client) applyInternalHost(req *http.Request) {
	host := strings.TrimSpace(c.publicHost)
	if host == "" {
		return
	}
	if strings.Contains(c.baseURL, host) {
		return
	}
	// docker/internal base URL: declare external host for upstream
	req.Host = host
	req.Header.Set("X-Forwarded-Host", host)
	req.Header.Set("X-Forwarded-Proto", "https")
}

// applyAdminAuth 支持两种管理员凭证：
// 1) Admin API Key（推荐，长期有效）→ Header: x-api-key
// 2) 管理员登录 JWT → Header: Authorization: Bearer <jwt>
// 判定：看起来像 JWT（两段点号）用 Bearer，否则用 x-api-key。
func (c *Client) applyAdminAuth(req *http.Request) {
	cred := c.AdminToken()
	if cred == "" {
		return
	}
	if looksLikeJWT(cred) {
		req.Header.Set("Authorization", "Bearer "+cred)
		return
	}
	req.Header.Set("x-api-key", cred)
}

func looksLikeJWT(s string) bool {
	// header.payload.signature
	parts := strings.Split(s, ".")
	return len(parts) == 3 && len(parts[0]) > 0 && len(parts[1]) > 0 && len(parts[2]) > 0
}

func (c *Client) applyClientMeta(req *http.Request, meta ClientMeta) {
	ip := firstNonEmpty(meta.CFConnecting, meta.XRealIP, meta.ClientIP)
	xff := firstNonEmpty(meta.ForwardedFor, ip)
	if ip != "" {
		req.Header.Set("X-Real-IP", ip)
		// 覆盖容器源 IP，让 sub2api 认为请求来自真实用户
		req.Header.Set("X-Forwarded-For", xff)
		req.Header.Set("True-Client-IP", ip)
		req.Header.Set("CF-Connecting-IP", ip)
	}
	if meta.UserAgent != "" {
		req.Header.Set("User-Agent", meta.UserAgent)
	}
	if meta.AcceptLang != "" {
		req.Header.Set("Accept-Language", meta.AcceptLang)
	}
}

func (c *Client) do(req *http.Request) ([]byte, int, error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return b, resp.StatusCode, nil
}

func decodeData(body []byte, dest any) error {
	var env apiEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return err
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return json.Unmarshal(body, dest)
	}
	return json.Unmarshal(env.Data, dest)
}

func extractErrorCode(body []byte) string {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return ""
	}
	if c, ok := m["code"]; ok {
		return fmt.Sprint(c)
	}
	return ""
}

// parseJWTClaims 不校验签名，仅读取 payload（浏览器已证明 token 可用）
func parseJWTClaims(token string) (jwtClaims, error) {
	var out jwtClaims
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return out, fmt.Errorf("not a jwt")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		payload, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return out, fmt.Errorf("decode payload: %w", err)
		}
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return out, err
	}
	for _, key := range []string{"user_id", "uid", "id", "sub"} {
		if v, ok := claims[key]; ok {
			if n := anyToInt64(v); n > 0 {
				out.UserID = n
				break
			}
		}
	}
	if out.UserID == 0 {
		return out, fmt.Errorf("user id claim not found in jwt")
	}
	if v, ok := claims["role"].(string); ok {
		out.Role = v
	}
	if v, ok := claims["email"].(string); ok {
		out.Email = v
	}
	if v, ok := claims["username"].(string); ok {
		out.Username = v
	}
	return out, nil
}

func anyToInt64(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case json.Number:
		n, _ := t.Int64()
		return n
	case string:
		if n, err := strconv.ParseInt(t, 10, 64); err == nil {
			return n
		}
		if i := strings.LastIndex(t, ":"); i >= 0 {
			if n, err := strconv.ParseInt(t[i+1:], 10, 64); err == nil {
				return n
			}
		}
	}
	return 0
}

func normalizeToken(userToken string) string {
	userToken = strings.TrimSpace(userToken)
	if userToken == "" {
		return ""
	}
	if len(userToken) > 7 && strings.EqualFold(userToken[:7], "Bearer ") {
		userToken = strings.TrimSpace(userToken[7:])
	}
	userToken = strings.Trim(userToken, `"'`)
	return strings.TrimSpace(userToken)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if v != "" {
			// X-Forwarded-For 可能是 a, b, c，取第一个
			if i := strings.Index(v, ","); i >= 0 {
				v = strings.TrimSpace(v[:i])
			}
			return v
		}
	}
	return ""
}

func truncate(b []byte, n int) string {
	s := string(b)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// sanitizeIdempotencyPart 把备注等文本压成合法 idempotency 片段（无空格、可打印 ASCII）
// Idempotency scopes for AddBalanceScoped.
const (
	IdempotencyScopeCheckin    = "checkin"
	IdempotencyScopeLottery    = "lottery"
	IdempotencyScopeRankReward = "rankreward"
	IdempotencyScopeTask       = "task"
)

// IdempotencyKey builds the upstream Idempotency-Key header value.
//
// sub2api requires printable ASCII without spaces, max 128 chars, so every
// dynamic part is sanitized. The "checkin" scope must keep producing exactly
// "checkin-<id>-<date>" for backward compatibility with already-issued keys.
func IdempotencyKey(scope string, userID int64, slot string) string {
	scopePart := sanitizeIdempotencyPart(scope)
	if scopePart == "" || scopePart == "na" {
		scopePart = IdempotencyScopeCheckin
	}
	slotPart := sanitizeIdempotencyPart(slot)
	if slotPart == "" || slotPart == "na" {
		slotPart = "unknown"
	}
	return fmt.Sprintf("%s-%d-%s", scopePart, userID, slotPart)
}

func sanitizeIdempotencyPart(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "na"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		case r == ' ' || r == '/' || r == ':':
			b.WriteByte('-')
		default:
			// skip non-ascii / other symbols
		}
		if b.Len() >= 80 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "na"
	}
	return out
}

func looksLikeIdempotentHit(status int, body []byte) bool {
	b := strings.ToLower(string(body))
	if strings.Contains(b, "idempoten") || strings.Contains(b, "duplicate") || strings.Contains(b, "already") {
		return true
	}
	// 部分网关对重复 key 返回 409
	return status == 409
}
