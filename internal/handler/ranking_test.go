package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sub2api-ext/internal/config"
	"sub2api-ext/internal/handler"
	"sub2api-ext/internal/settings"
	"sub2api-ext/internal/store"
	"sub2api-ext/internal/sub2api"
)

func TestRewardsRankingAPI(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, err := st.TryInsert(ctx, 7, "2026-07-30", 0.3, 1); err != nil {
		t.Fatal(err)
	}
	id, err := st.ReserveLotteryDraw(ctx, 8, "2026-07-30")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FinalizeLotteryDraw(ctx, id, "奖", store.PrizeTypeBalance, 1.2, 2); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{}
	cfg.Checkin.Timezone = "Asia/Shanghai"
	cfg.Checkin.Enabled = true
	cfg.Checkin.RewardAmount = 0.1
	cfg.Security.RateStatusPerMin = 1000
	cfg.Security.RateCheckinPerMin = 1000
	cfg.Security.RateAdminReadPerMin = 1000
	cfg.Security.RateAdminWritePerMin = 1000
	stg := settings.New(st, cfg.Checkin)
	client := sub2api.New("http://127.0.0.1:9", "", time.Second)
	h := handler.New(cfg, st, client, stg, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/ranking/rewards?range=2026-07-30,2026-07-30&limit=20", nil)
	rec := httptest.NewRecorder()
	h.RankingRewards(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["board"] != "rewards" {
		t.Fatalf("board=%v", body["board"])
	}
	items, _ := body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items=%v", rec.Body.String())
	}
	top := items[0].(map[string]any)
	if int(top["rank"].(float64)) != 1 {
		t.Fatalf("top rank=%v", top["rank"])
	}
	if int64(top["user_id"].(float64)) != 8 {
		t.Fatalf("top user=%v", top)
	}
	name, _ := top["display_name"].(string)
	if name == "" || !strings.Contains(name, "***") {
		t.Fatalf("expected masked name, got %q", name)
	}
}

func TestConsumptionRankingFallback(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := config.Config{}
	cfg.Checkin.Timezone = "Asia/Shanghai"
	cfg.Security.RateStatusPerMin = 1000
	cfg.Security.RateCheckinPerMin = 1000
	cfg.Security.RateAdminReadPerMin = 1000
	cfg.Security.RateAdminWritePerMin = 1000
	stg := settings.New(st, cfg.Checkin)
	client := sub2api.NewWithPublicHost("http://127.0.0.1:9", "", "sub2api.example.com", time.Second)
	h := handler.New(cfg, st, client, stg, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/ranking/consumption?range=today", nil)
	rec := httptest.NewRecorder()
	h.RankingConsumption(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	fb, _ := body["fallback_url"].(string)
	if !strings.Contains(fb, "sub2api.example.com/rank") {
		t.Fatalf("fallback=%v body=%v", fb, body)
	}
	if body["warning"] == nil || body["warning"] == "" {
		t.Fatalf("expected warning when upstream down: %v", body)
	}
}

func TestConsumptionRankingUsesTokenVolume(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/admin/dashboard/users-ranking" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("limit"); got != "5000" {
			t.Fatalf("upstream limit=%s want 5000", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"total_actual_cost":17,"total_requests":5,"total_tokens":500,"ranking":[` +
			`{"user_id":1,"username":"cost-first","actual_cost":10,"requests":2,"tokens":100},` +
			`{"user_id":2,"username":"token-first","actual_cost":7,"requests":3,"tokens":400}]}}`))
	}))
	defer upstream.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "token-rank.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := config.Default()
	cfg.Sub2API.BaseURL = upstream.URL
	cfg.Sub2API.AdminToken = "admin-key"
	cfg.Checkin.Timezone = "Asia/Shanghai"
	client := sub2api.New(upstream.URL, cfg.Sub2API.AdminToken, time.Second)
	h := handler.New(cfg, st, client, settings.New(st, cfg.Checkin), nil)

	req := httptest.NewRequest(http.MethodGet, "/api/ranking/consumption?range=2026-08-12,2026-08-12&limit=20", nil)
	rec := httptest.NewRecorder()
	h.RankingConsumption(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []struct {
			Rank                int     `json:"rank"`
			UserID              int64   `json:"user_id"`
			TokenCount          float64 `json:"token_count"`
			TokenShare          float64 `json:"token_share"`
			AvgTokensPerRequest float64 `json:"avg_tokens_per_request"`
		} `json:"items"`
		Summary struct {
			TotalTokens    float64 `json:"total_tokens"`
			TotalRequests  int64   `json:"total_requests"`
			TopTokens      float64 `json:"top_tokens"`
			Top3TokenShare float64 `json:"top3_token_share"`
			UserCount      int64   `json:"user_count"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 2 || body.Items[0].UserID != 2 || body.Items[0].Rank != 1 {
		t.Fatalf("items=%+v", body.Items)
	}
	if body.Items[0].TokenCount != 400 || body.Items[0].TokenShare != 0.8 || body.Items[0].AvgTokensPerRequest != 400.0/3.0 {
		t.Fatalf("top metrics=%+v", body.Items[0])
	}
	if body.Summary.TotalTokens != 500 || body.Summary.TotalRequests != 5 || body.Summary.TopTokens != 400 || body.Summary.Top3TokenShare != 1 || body.Summary.UserCount != 2 {
		t.Fatalf("summary=%+v", body.Summary)
	}
}
