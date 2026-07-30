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
