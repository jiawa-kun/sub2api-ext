package redistribution

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sub2api-ext/internal/credit"
	"sub2api-ext/internal/store"
	"sub2api-ext/internal/sub2api"
)

type testBalanceState struct {
	mu       sync.Mutex
	balances map[int64]float64
}

func (s *testBalanceState) balance(id int64) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.balances[id]
}

func TestPreviewAndExecuteAutoRedistribution(t *testing.T) {
	now := time.Now().UTC()
	old := now.AddDate(0, 0, -90)
	recent := now.AddDate(0, 0, -1)
	created := now.AddDate(0, 0, -120)

	upstream := &testBalanceState{balances: map[int64]float64{1: 2, 2: 0}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/admin/users":
			items := []map[string]any{
				{"id": 1, "username": "idle", "role": "user", "status": "active", "balance": upstream.balance(1), "created_at": created, "last_active_at": old, "last_used_at": old, "total_recharged": 0},
				{"id": 2, "username": "active", "role": "user", "status": "active", "balance": upstream.balance(2), "created_at": created, "last_active_at": recent, "last_used_at": recent, "total_recharged": 0},
			}
			writeTestJSON(w, map[string]any{"code": 0, "data": map[string]any{"items": items, "total": 2, "page": 1, "page_size": 100, "pages": 1}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/admin/dashboard/users-usage":
			writeTestJSON(w, map[string]any{"code": 0, "data": map[string]any{"stats": map[string]any{
				"1": map[string]any{"user_id": 1, "total_actual_cost": 0},
				"2": map[string]any{"user_id": 2, "total_actual_cost": 10},
			}}})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/admin/dashboard/users-ranking"):
			writeTestJSON(w, map[string]any{"code": 0, "data": map[string]any{"ranking": []map[string]any{{"user_id": 2, "actual_cost": 10}}}})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/admin/users/"):
			var id int64
			_, _ = fmt.Sscanf(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/users/"), "%d", &id)
			last := recent
			if id == 1 {
				last = old
			}
			writeTestJSON(w, map[string]any{"code": 0, "data": map[string]any{
				"id": id, "username": fmt.Sprintf("u%d", id), "role": "user", "status": "active",
				"balance": upstream.balance(id), "created_at": created, "last_active_at": last, "last_used_at": last,
			}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/balance"):
			var id int64
			_, _ = fmt.Sscanf(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/users/"), "/balance"), "%d", &id)
			var body struct {
				Balance   float64 `json:"balance"`
				Operation string  `json:"operation"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			upstream.mu.Lock()
			if body.Operation == "subtract" {
				upstream.balances[id] -= body.Balance
			} else if body.Operation == "add" {
				upstream.balances[id] += body.Balance
			}
			balance := upstream.balances[id]
			upstream.mu.Unlock()
			writeTestJSON(w, map[string]any{"code": 0, "data": map[string]any{"id": id, "balance": balance, "role": "user", "status": "active"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "redistribution.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	client := sub2api.New(server.URL, "admin-key", 5*time.Second)
	creditSvc := credit.New(st, client)
	settings := NewSettings(st)
	rt := settings.Get()
	rt.Enabled = true
	rt.NewUserProtectionDays = 0
	rt.Reclaim.Mode = ReclaimFixed
	rt.Reclaim.Value = 0.5
	rt.Reclaim.MinBalance = 1
	rt.Reclaim.ReserveBalance = 0.5
	rt.Reclaim.MaxPerUser = 1
	rt.Allocation.Mode = AllocationEqual
	rt.Allocation.MaxRewardPerUser = 1
	if _, err := settings.Save(context.Background(), rt); err != nil {
		t.Fatal(err)
	}
	svc := NewService(st, client, creditSvc, settings, nil)

	preview, err := svc.Preview(context.Background(), "manual", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Donors) != 1 || preview.Donors[0].UserID != 1 {
		t.Fatalf("donors=%+v", preview.Donors)
	}
	if len(preview.Rewards) != 1 || preview.Rewards[0].UserID != 2 {
		t.Fatalf("rewards=%+v", preview.Rewards)
	}
	result, err := svc.Execute(context.Background(), preview.Batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Batch.Status != store.RedistributionBatchSuccess {
		t.Fatalf("status=%s err=%s", result.Batch.Status, result.Batch.Error)
	}
	if got := upstream.balance(1); got != 1.5 {
		t.Fatalf("donor balance=%v", got)
	}
	if got := upstream.balance(2); got != 0.5 {
		t.Fatalf("recipient balance=%v", got)
	}
	ledger, err := st.ListLedger(context.Background(), store.LedgerFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger) != 2 {
		t.Fatalf("ledger=%+v", ledger)
	}
	amounts := map[string]float64{}
	for _, row := range ledger {
		amounts[row.Source] += row.Amount
	}
	if amounts[store.LedgerSourceInactiveReclaim] != -0.5 || amounts[store.LedgerSourceRedistribution] != 0.5 {
		t.Fatalf("ledger amounts=%+v", amounts)
	}
}

func TestExecuteClaimModeAndClaimCompletesBatch(t *testing.T) {
	now := time.Now().UTC()
	old := now.AddDate(0, 0, -90)
	recent := now.AddDate(0, 0, -1)
	created := now.AddDate(0, 0, -120)

	upstream := &testBalanceState{balances: map[int64]float64{1: 2, 2: 0}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/admin/users":
			items := []map[string]any{
				{"id": 1, "username": "idle", "role": "user", "status": "active", "balance": upstream.balance(1), "created_at": created, "last_active_at": old, "last_used_at": old, "total_recharged": 0},
				{"id": 2, "username": "active", "role": "user", "status": "active", "balance": upstream.balance(2), "created_at": created, "last_active_at": recent, "last_used_at": recent, "total_recharged": 0},
			}
			writeTestJSON(w, map[string]any{"code": 0, "data": map[string]any{"items": items, "total": 2, "page": 1, "page_size": 100, "pages": 1}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/admin/dashboard/users-usage":
			writeTestJSON(w, map[string]any{"code": 0, "data": map[string]any{"stats": map[string]any{
				"1": map[string]any{"user_id": 1, "total_actual_cost": 0},
				"2": map[string]any{"user_id": 2, "total_actual_cost": 10},
			}}})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/admin/dashboard/users-ranking"):
			writeTestJSON(w, map[string]any{"code": 0, "data": map[string]any{"ranking": []map[string]any{{"user_id": 2, "actual_cost": 10}}}})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/admin/users/"):
			var id int64
			_, _ = fmt.Sscanf(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/users/"), "%d", &id)
			last := recent
			if id == 1 {
				last = old
			}
			writeTestJSON(w, map[string]any{"code": 0, "data": map[string]any{
				"id": id, "username": fmt.Sprintf("u%d", id), "role": "user", "status": "active",
				"balance": upstream.balance(id), "created_at": created, "last_active_at": last, "last_used_at": last,
			}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/balance"):
			var id int64
			_, _ = fmt.Sscanf(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/users/"), "/balance"), "%d", &id)
			var body struct {
				Balance   float64 `json:"balance"`
				Operation string  `json:"operation"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			upstream.mu.Lock()
			if body.Operation == "subtract" {
				upstream.balances[id] -= body.Balance
			} else if body.Operation == "add" {
				upstream.balances[id] += body.Balance
			}
			balance := upstream.balances[id]
			upstream.mu.Unlock()
			writeTestJSON(w, map[string]any{"code": 0, "data": map[string]any{"id": id, "balance": balance, "role": "user", "status": "active"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "redistribution-claim.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	client := sub2api.New(server.URL, "admin-key", 5*time.Second)
	creditSvc := credit.New(st, client)
	settings := NewSettings(st)
	rt := settings.Get()
	rt.Enabled = true
	rt.NewUserProtectionDays = 0
	rt.Reclaim.Mode = ReclaimFixed
	rt.Reclaim.Value = 0.5
	rt.Reclaim.MinBalance = 1
	rt.Reclaim.ReserveBalance = 0.5
	rt.Reclaim.MaxPerUser = 1
	rt.DistributionMode = DistributionClaim
	rt.ClaimExpireDays = 3
	rt.Allocation.Mode = AllocationEqual
	rt.Allocation.MaxRewardPerUser = 1
	if _, err := settings.Save(context.Background(), rt); err != nil {
		t.Fatal(err)
	}
	svc := NewService(st, client, creditSvc, settings, nil)

	preview, err := svc.Preview(context.Background(), "manual", now)
	if err != nil {
		t.Fatal(err)
	}
	executed, err := svc.Execute(context.Background(), preview.Batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if executed.Batch.Status != store.RedistributionBatchAwaitingClaim {
		t.Fatalf("status=%s", executed.Batch.Status)
	}
	if got := upstream.balance(2); got != 0 {
		t.Fatalf("recipient should not receive before claim, balance=%v", got)
	}
	reward, err := svc.Claim(context.Background(), 2, preview.Batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reward.Status != store.RedistributionEntryClaimed || reward.ActualAmount != 0.5 {
		t.Fatalf("reward=%+v", reward)
	}
	if got := upstream.balance(2); got != 0.5 {
		t.Fatalf("recipient balance=%v", got)
	}
	detail, err := svc.Detail(context.Background(), preview.Batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Batch.Status != store.RedistributionBatchSuccess || detail.Batch.ActualDistribute != 0.5 {
		t.Fatalf("batch=%+v", detail.Batch)
	}
	if pool, err := st.RedistributionAvailablePool(context.Background()); err != nil || pool != 0 {
		t.Fatalf("pool=%v err=%v", pool, err)
	}
}

func writeTestJSON(w http.ResponseWriter, v any) {
	_ = json.NewEncoder(w).Encode(v)
}
