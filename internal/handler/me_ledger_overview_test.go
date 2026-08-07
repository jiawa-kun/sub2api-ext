package handler_test

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestMeLedgerPinsUserAndIgnoresQueryUserID(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/auth/me") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"id":7,"username":"tester","role":"user","balance":12}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(upstream.Close)

	st, err := store.Open(filepath.Join(t.TempDir(), "me-ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	// user 7 success + user 8 success (must not leak)
	if _, err := st.InsertLedger(ctx, store.LedgerEntry{
		UserID: 7, Source: store.LedgerSourceCheckin, Amount: 0.5,
		IdempotencyKey: "c7", Status: store.LedgerStatusSuccess, Notes: "mine",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertLedger(ctx, store.LedgerEntry{
		UserID: 8, Source: store.LedgerSourceLottery, Amount: 9,
		IdempotencyKey: "c8", Status: store.LedgerStatusSuccess, Notes: "other",
	}); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{}
	cfg.Sub2API.BaseURL = upstream.URL
	cfg.Sub2API.AdminToken = "admin-key"
	cfg.Checkin.Timezone = "Asia/Shanghai"
	cfg.Security.RateStatusPerMin = 1000
	cfg.Security.RateCheckinPerMin = 1000
	cfg.Security.RateAdminReadPerMin = 1000
	cfg.Security.RateAdminWritePerMin = 1000
	stg := settings.New(st, cfg.Checkin)
	client := sub2api.New(upstream.URL, "admin-key", time.Second)
	h := handler.New(cfg, st, client, stg, nil)

	// no token
	req := httptest.NewRequest(http.MethodGet, "/api/me/ledger", nil)
	rec := httptest.NewRecorder()
	h.MeLedger(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token status=%d body=%s", rec.Code, rec.Body.String())
	}

	// with token but forged user_id=8
	req = httptest.NewRequest(http.MethodGet, "/api/me/ledger?user_id=8&limit=50", nil)
	req.Header.Set("Authorization", "Bearer user-token")
	rec = httptest.NewRecorder()
	h.MeLedger(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if int64(body["user_id"].(float64)) != 7 {
		t.Fatalf("user_id=%v", body["user_id"])
	}
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items=%v", rec.Body.String())
	}
	row := items[0].(map[string]any)
	if int64(row["user_id"].(float64)) != 7 {
		t.Fatalf("leaked row=%v", row)
	}
	if _, ok := row["idempotency_key"]; ok {
		t.Fatalf("idempotency_key should be hidden: %v", row)
	}
	if body["success_amount"].(float64) != 0.5 {
		t.Fatalf("success_amount=%v", body["success_amount"])
	}

	for i := 0; i < 11; i++ {
		if _, err := st.InsertLedger(ctx, store.LedgerEntry{
			UserID: 7, Source: store.LedgerSourceCheckin, Amount: 1,
			IdempotencyKey: fmt.Sprintf("extra-%d", i), Status: store.LedgerStatusSuccess,
		}); err != nil {
			t.Fatal(err)
		}
	}

	req = httptest.NewRequest(http.MethodGet, "/api/me/ledger", nil)
	req.Header.Set("Authorization", "Bearer user-token")
	rec = httptest.NewRecorder()
	h.MeLedger(rec, req)
	body = map[string]any{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if int(body["limit"].(float64)) != 10 || len(body["items"].([]any)) != 10 || int(body["total"].(float64)) != 12 {
		t.Fatalf("me ledger default paging=%s", rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/admin/ledger", nil)
	req.Header.Set("x-api-key", "admin-key")
	rec = httptest.NewRecorder()
	h.AdminListLedger(rec, req)
	body = map[string]any{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if int(body["limit"].(float64)) != 10 || len(body["items"].([]any)) != 10 || int(body["total"].(float64)) != 13 {
		t.Fatalf("admin ledger default paging=%s", rec.Body.String())
	}
}

func TestAdminOverviewRequiresAuthAndReturnsBlocks(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ov.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	today := time.Now().In(time.FixedZone("CST", 8*3600)).Format("2006-01-02")
	// force created_at date prefix via Insert then raw? Insert uses now UTC — OK for today mostly.
	if _, err := st.InsertLedger(ctx, store.LedgerEntry{
		UserID: 1, Source: store.LedgerSourceTask, Amount: 1.25,
		IdempotencyKey: "t1", Status: store.LedgerStatusSuccess,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertLedger(ctx, store.LedgerEntry{
		UserID: 2, Source: store.LedgerSourceCheckin, Amount: 0.1,
		IdempotencyKey: "t2", Status: store.LedgerStatusFailed, Error: "boom",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateRankCampaign(ctx, store.RankCampaign{
		Name: "demo", Board: store.CampaignBoardRewards,
		StartDate: today, EndDate: today, TopN: 3,
		RewardsJSON: `[{"rank":1,"amount":1}]`, Status: store.CampaignStatusActive,
	}); err != nil {
		t.Fatal(err)
	}

	const adminKey = "admin-ov-key"
	cfg := config.Config{}
	cfg.Sub2API.AdminToken = adminKey
	cfg.Checkin.Timezone = "Asia/Shanghai"
	cfg.Checkin.Enabled = true
	cfg.Security.RateAdminReadPerMin = 1000
	cfg.Security.RateAdminWritePerMin = 1000
	cfg.Security.RateStatusPerMin = 1000
	cfg.Security.RateCheckinPerMin = 1000
	stg := settings.New(st, cfg.Checkin)
	client := sub2api.New("http://127.0.0.1:9", adminKey, time.Second)
	h := handler.New(cfg, st, client, stg, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/overview", nil)
	rec := httptest.NewRecorder()
	h.AdminOverview(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/admin/overview", nil)
	req.Header.Set("x-api-key", adminKey)
	rec = httptest.NewRecorder()
	h.AdminOverview(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"ledger_today", "checkin", "lottery", "campaigns", "patrol", "links"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("missing %s in %v", key, body)
		}
	}
	led := body["ledger_today"].(map[string]any)
	if led["available"] != true {
		t.Fatalf("ledger_today=%v", led)
	}
	cam := body["campaigns"].(map[string]any)
	if int(cam["active_count"].(float64)) < 1 {
		t.Fatalf("active_count=%v body=%s", cam["active_count"], rec.Body.String())
	}
}
