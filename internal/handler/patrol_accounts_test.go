package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"sub2api-ext/internal/config"
	"sub2api-ext/internal/handler"
	"sub2api-ext/internal/patrol"
	"sub2api-ext/internal/settings"
	"sub2api-ext/internal/store"
	"sub2api-ext/internal/sub2api"
)

func TestAdminPatrolAccountsEndpoint(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	const adminKey = "admin-test-key"
	cfg := config.Config{}
	cfg.Sub2API.BaseURL = "http://127.0.0.1:1"
	cfg.Sub2API.AdminToken = adminKey
	cfg.Checkin.Timezone = "Asia/Shanghai"
	cfg.Patrol.FailThreshold = 2
	cfg.Patrol.Groups = []string{"group-a"}
	cfg.Patrol.Cron = "0 */6 * * *"
	cfg.Patrol.Concurrency = 1
	cfg.Patrol.TimeoutMs = 3000
	cfg.Patrol.ActionOnFail = "disable"
	cfg.Patrol.Timezone = "Asia/Shanghai"

	client := sub2api.New(cfg.Sub2API.BaseURL, adminKey, time.Second)
	stg := settings.New(st, cfg.Checkin)
	ps := patrol.NewSettings(st, cfg.Patrol)
	svc := patrol.NewService(client, st, ps)
	h := handler.New(cfg, st, client, stg, svc)

	ctx := context.Background()
	if _, err := st.UpsertPatrolAccountFail(ctx, 101, "acc-101", "group-a", "模型异常"); err != nil {
		t.Fatal(err)
	}

	// unauthenticated request must be rejected
	req := httptest.NewRequest(http.MethodGet, "/api/admin/patrol/accounts", nil)
	rec := httptest.NewRecorder()
	h.AdminPatrolAccounts(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", rec.Code)
	}

	// authenticated request returns the health rows
	req = httptest.NewRequest(http.MethodGet, "/api/admin/patrol/accounts?only_problem=1", nil)
	req.Header.Set("x-api-key", adminKey)
	rec = httptest.NewRecorder()
	h.AdminPatrolAccounts(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}

	var out struct {
		Count         int  `json:"count"`
		FailThreshold int  `json:"fail_threshold"`
		OnlyProblem   bool `json:"only_problem"`
		Items         []struct {
			AccountID       int64  `json:"account_id"`
			ConsecutiveFail int    `json:"consecutive_fail"`
			LastReason      string `json:"last_reason"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if out.Count != 1 || len(out.Items) != 1 {
		t.Fatalf("count = %d items = %d", out.Count, len(out.Items))
	}
	if out.Items[0].AccountID != 101 || out.Items[0].ConsecutiveFail != 1 {
		t.Fatalf("item = %+v", out.Items[0])
	}
	if out.FailThreshold != 2 {
		t.Fatalf("fail_threshold = %d, want 2", out.FailThreshold)
	}
	if !out.OnlyProblem {
		t.Fatal("only_problem should be true")
	}
}
