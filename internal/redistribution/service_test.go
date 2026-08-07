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
	// fixed away from any timezone day boundary so now+1h stays on the same draw date
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
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
	rt.DrawMode = DrawFixed
	rt.DrawFixedAmount = 0.2
	rt.PoolExpireDays = 9
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
	if len(preview.Rewards) != 0 {
		t.Fatalf("new pool batch must not create recipients: %+v", preview.Rewards)
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
	if got := upstream.balance(2); got != 0 {
		t.Fatalf("active user must draw explicitly, balance=%v", got)
	}
	ledger, err := st.ListLedger(context.Background(), store.LedgerFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger) != 1 {
		t.Fatalf("ledger=%+v", ledger)
	}
	amounts := map[string]float64{}
	for _, row := range ledger {
		amounts[row.Source] += row.Amount
	}
	if amounts[store.LedgerSourceInactiveReclaim] != -0.5 || amounts[store.LedgerSourceRedistribution] != 0 {
		t.Fatalf("ledger amounts=%+v", amounts)
	}
	lots, err := st.ListRedistributionPoolLots(context.Background(), 0, false, 10)
	if err != nil || len(lots) != 1 || lots[0].SourceUserID != 1 || lots[0].RemainingAmount != 0.5 {
		t.Fatalf("lots=%+v err=%v", lots, err)
	}
	poolResult, err := svc.Pool(context.Background(), 1, now)
	if err != nil {
		t.Fatal(err)
	}
	if poolResult.Rules.DrawMode != DrawFixed || poolResult.Rules.DrawFixedAmount != 0.2 || poolResult.Rules.PoolExpireDays != 9 {
		t.Fatalf("public pool rules=%+v", poolResult.Rules)
	}
	if !poolResult.Rules.PaidBalanceProtected || !poolResult.Rules.DrawIncludesWeekends || !poolResult.Rules.CanRecoverBeforeExpiry {
		t.Fatalf("public protections=%+v", poolResult.Rules)
	}
	if poolResult.Rules.EffectiveAt == "" || len(poolResult.Rules.InactiveRules) == 0 || len(poolResult.Rules.ActiveRules) == 0 {
		t.Fatalf("public rule metadata=%+v", poolResult.Rules)
	}
	drawn, err := svc.Draw(context.Background(), 2, now)
	if err != nil || drawn.Draw.Amount != 0.2 {
		t.Fatalf("draw=%+v err=%v", drawn, err)
	}
	if got := upstream.balance(2); got != 0.2 {
		t.Fatalf("draw balance=%v", got)
	}
	again, err := svc.Draw(context.Background(), 2, now.Add(time.Hour))
	if err != nil || again.Draw.ID != drawn.Draw.ID || upstream.balance(2) != 0.2 {
		t.Fatalf("daily draw must be idempotent: again=%+v err=%v balance=%v", again, err, upstream.balance(2))
	}
	refunds, err := svc.Recover(context.Background(), 1, now.Add(2*time.Hour))
	if err != nil || len(refunds) != 1 || refunds[0].Amount != 0.3 {
		t.Fatalf("refunds=%+v err=%v", refunds, err)
	}
	if got := upstream.balance(1); got != 1.8 {
		t.Fatalf("recovered balance=%v", got)
	}
	if pool, err := st.RedistributionAvailablePool(context.Background()); err != nil || pool != 0 {
		t.Fatalf("pool after recover=%v err=%v", pool, err)
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
	if executed.Batch.Status != store.RedistributionBatchSuccess {
		t.Fatalf("status=%s", executed.Batch.Status)
	}
	if got := upstream.balance(2); got != 0 {
		t.Fatalf("recipient should not receive before claim, balance=%v", got)
	}
	if pool, err := st.RedistributionAvailablePool(context.Background()); err != nil || pool != 0.5 {
		t.Fatalf("pool=%v err=%v", pool, err)
	}
}

func TestRefundExpiredPoolLot(t *testing.T) {
	now := time.Now().UTC()
	upstream := &testBalanceState{balances: map[int64]float64{9: 0}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/balance"):
			var body struct {
				Balance   float64 `json:"balance"`
				Operation string  `json:"operation"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			upstream.mu.Lock()
			upstream.balances[9] += body.Balance
			balance := upstream.balances[9]
			upstream.mu.Unlock()
			writeTestJSON(w, map[string]any{"code": 0, "data": map[string]any{"id": 9, "balance": balance, "role": "user", "status": "active"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	st, err := store.Open(filepath.Join(t.TempDir(), "expired-refund.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	batchID, err := st.CreateRedistributionBatch(context.Background(), store.RedistributionBatch{Status: store.RedistributionBatchSuccess, ActualReclaim: 1, CreatedAt: now.Add(-48 * time.Hour)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateRedistributionPoolLot(context.Background(), store.RedistributionPoolLot{SourceBatchID: batchID, SourceUserID: 9, OriginalAmount: 1, CreatedAt: now.Add(-48 * time.Hour), ExpiresAt: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	client := sub2api.New(server.URL, "admin-key", 5*time.Second)
	svc := NewService(st, client, credit.New(st, client), NewSettings(st), nil)
	if err := svc.RefundExpired(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if got := upstream.balance(9); got != 1 {
		t.Fatalf("refund balance=%v", got)
	}
	if pool, err := st.RedistributionAvailablePool(context.Background()); err != nil || pool != 0 {
		t.Fatalf("pool=%v err=%v", pool, err)
	}
	if err := svc.RefundExpired(context.Background(), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := upstream.balance(9); got != 1 {
		t.Fatalf("refund must be idempotent, balance=%v", got)
	}
}

func writeTestJSON(w http.ResponseWriter, v any) {
	_ = json.NewEncoder(w).Encode(v)
}
