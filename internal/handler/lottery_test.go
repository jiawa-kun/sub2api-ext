package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sub2api-ext/internal/config"
	"sub2api-ext/internal/handler"
	"sub2api-ext/internal/lottery"
	"sub2api-ext/internal/patrol"
	"sub2api-ext/internal/settings"
	"sub2api-ext/internal/store"
	"sub2api-ext/internal/sub2api"
)

const lotteryUserToken = "user-token-abc"

type lotteryFixture struct {
	h        *handler.Handler
	st       *store.Store
	lot      *lottery.Settings
	stg      *settings.Service
	upstream *httptest.Server
	// credits records every balance grant the upstream received.
	credits []creditCall
	mu      sync.Mutex
}

type creditCall struct {
	UserID         int64
	Amount         float64
	IdempotencyKey string
}

// newLotteryFixture wires the real handler against a fake sub2api so the whole
// draw path (auth -> reserve -> credit -> finalize) is exercised.
func newLotteryFixture(t *testing.T, rt lottery.Runtime) *lotteryFixture {
	t.Helper()
	f := &lotteryFixture{}

	balance := 100.0
	f.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/auth/me"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"id":7,"username":"tester","role":"user","balance":100}}`))
		case strings.Contains(r.URL.Path, "/balance"):
			var body struct {
				Balance float64 `json:"balance"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			balance += body.Balance
			f.credits = append(f.credits, creditCall{
				UserID:         7,
				Amount:         body.Balance,
				IdempotencyKey: r.Header.Get("Idempotency-Key"),
			})
			cur := balance
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"id":7,"username":"tester","role":"user","balance":` + trimFloat(cur) + `}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.upstream.Close)

	st, err := store.Open(filepath.Join(t.TempDir(), "lot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f.st = st

	cfg := config.Config{}
	cfg.Sub2API.BaseURL = f.upstream.URL
	cfg.Sub2API.AdminToken = "admin-key"
	cfg.Checkin.Timezone = "Asia/Shanghai"
	cfg.Patrol.Cron = "0 */6 * * *"
	cfg.Patrol.Timezone = "Asia/Shanghai"
	cfg.Security.RateCheckinPerMin = 100
	cfg.Security.RateStatusPerMin = 100
	cfg.Security.RateAdminReadPerMin = 100
	cfg.Security.RateAdminWritePerMin = 100

	client := sub2api.New(cfg.Sub2API.BaseURL, "admin-key", 5*time.Second)
	f.stg = settings.New(st, cfg.Checkin)
	ps := patrol.NewSettings(st, cfg.Patrol)
	svc := patrol.NewService(client, st, ps)

	f.lot = lottery.NewSettings(st, config.LotteryConfig{})
	if _, err := f.lot.Update(context.Background(), lottery.UpdateInput{
		Enabled:        &rt.Enabled,
		RequireCheckin: &rt.RequireCheckin,
		Prizes:         &rt.Prizes,
		DailyBudget:    &rt.DailyBudget,
		HardCap:        &rt.HardCap,
	}); err != nil {
		t.Fatal(err)
	}

	f.h = handler.New(cfg, st, client, f.stg, svc)
	f.h.SetLottery(f.lot)
	return f
}

func trimFloat(v float64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func (f *lotteryFixture) draw(t *testing.T, withToken bool) (int, lotteryDrawBody) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/lottery/draw", nil)
	if withToken {
		req.Header.Set("Authorization", "Bearer "+lotteryUserToken)
	}
	rec := httptest.NewRecorder()
	f.h.LotteryDraw(rec, req)
	var body lotteryDrawBody
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

type lotteryDrawBody struct {
	Status     string  `json:"status"`
	Message    string  `json:"message"`
	PrizeLabel string  `json:"prize_label"`
	PrizeIndex *int    `json:"prize_index"`
	Amount     float64 `json:"amount"`
	NewBalance float64 `json:"new_balance"`
	DrawnToday bool    `json:"drawn_today"`
	Error      string  `json:"error"`
}

type lotteryStatusBody struct {
	Enabled        bool   `json:"enabled"`
	CanDraw        bool   `json:"can_draw"`
	DrawnToday     bool   `json:"drawn_today"`
	CheckedInToday bool   `json:"checked_in_today"`
	Reason         string `json:"reason"`
	Prizes         []struct {
		Label  string  `json:"label"`
		Amount float64 `json:"amount"`
	} `json:"prizes"`
	TodayPrize string `json:"today_prize"`
}

func alwaysWinPool() lottery.Runtime {
	return lottery.Runtime{
		Enabled:        true,
		RequireCheckin: false,
		Prizes:         []lottery.Prize{{Label: "必中 2 额度", Amount: 2, Weight: 1}},
	}
}

func TestLotteryDrawCreditsAndIsOncePerDay(t *testing.T) {
	f := newLotteryFixture(t, alwaysWinPool())

	code, body := f.draw(t, true)
	if code != http.StatusOK || body.Status != "win" {
		t.Fatalf("first draw: code=%d body=%+v", code, body)
	}
	if body.Amount != 2 {
		t.Fatalf("amount = %v, want 2", body.Amount)
	}
	if body.PrizeIndex == nil || *body.PrizeIndex != 0 {
		t.Fatalf("prize index = %v, want visible index 0", body.PrizeIndex)
	}
	if body.NewBalance != 102 {
		t.Fatalf("new balance = %v, want 102", body.NewBalance)
	}

	// second draw same day is rejected and must not credit again
	code, body = f.draw(t, true)
	if code != http.StatusOK || body.Status != "already" {
		t.Fatalf("second draw: code=%d body=%+v", code, body)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.credits) != 1 {
		t.Fatalf("credited %d times, want exactly 1", len(f.credits))
	}
	if !strings.HasPrefix(f.credits[0].IdempotencyKey, "lottery-7-") {
		t.Fatalf("idempotency key = %q, want lottery scope", f.credits[0].IdempotencyKey)
	}
}

func TestLotteryDrawMapsHiddenPrizeIndexForPublicResponse(t *testing.T) {
	rt := lottery.Runtime{
		Enabled: true,
		Prizes: []lottery.Prize{
			{Label: "隐藏项", Amount: 99, Weight: 0},
			{Label: "可中奖项", Amount: 2, Weight: 1},
		},
	}
	f := newLotteryFixture(t, rt)
	_, body := f.draw(t, true)
	if body.Status != "win" {
		t.Fatalf("draw failed: %+v", body)
	}
	if body.PrizeLabel != "可中奖项" || body.PrizeIndex == nil || *body.PrizeIndex != 0 {
		t.Fatalf("public prize position = %+v, want visible index 0: %+v", body.PrizeIndex, body)
	}
}

// Two simultaneous draws must produce exactly one credit.
func TestLotteryDrawConcurrentSingleCredit(t *testing.T) {
	f := newLotteryFixture(t, alwaysWinPool())

	const n = 8
	var wg sync.WaitGroup
	results := make([]string, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			_, body := f.draw(t, true)
			results[idx] = body.Status
		}(i)
	}
	wg.Wait()

	wins := 0
	for _, s := range results {
		if s == "win" || s == "miss" {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("fresh draws = %d, want 1 (results: %v)", wins, results)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.credits) != 1 {
		t.Fatalf("credited %d times under concurrency, want 1", len(f.credits))
	}
}

func TestLotteryDrawRequiresToken(t *testing.T) {
	f := newLotteryFixture(t, alwaysWinPool())
	code, _ := f.draw(t, false)
	if code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", code)
	}
}

func TestLotteryDrawDisabledIsNoop(t *testing.T) {
	rt := alwaysWinPool()
	rt.Enabled = false
	f := newLotteryFixture(t, rt)

	_, body := f.draw(t, true)
	if body.Status != "disabled" {
		t.Fatalf("status = %q, want disabled", body.Status)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.credits) != 0 {
		t.Fatal("disabled module must not credit")
	}
}

func TestLotteryDrawRequiresCheckinWhenConfigured(t *testing.T) {
	rt := alwaysWinPool()
	rt.RequireCheckin = true
	f := newLotteryFixture(t, rt)

	_, body := f.draw(t, true)
	if body.Status != "need_checkin" {
		t.Fatalf("status = %q, want need_checkin", body.Status)
	}

	// after checking in, the draw goes through
	if _, err := f.st.TryInsert(context.Background(), 7, f.stg.Today(), 1, 101); err != nil {
		t.Fatal(err)
	}
	_, body = f.draw(t, true)
	if body.Status != "win" {
		t.Fatalf("status after check-in = %q, want win", body.Status)
	}
}

func TestLotteryDrawBudgetExhaustedClosesEntrance(t *testing.T) {
	rt := alwaysWinPool()
	rt.DailyBudget = 3
	f := newLotteryFixture(t, rt)
	ctx := context.Background()

	// pre-spend the whole budget with another user
	id, err := f.st.ReserveLotteryDraw(ctx, 999, f.stg.Today())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.FinalizeLotteryDraw(ctx, id, "x", store.PrizeTypeBalance, 3, 0); err != nil {
		t.Fatal(err)
	}

	_, body := f.draw(t, true)
	if body.Status != "budget_exhausted" {
		t.Fatalf("status = %q, want budget_exhausted", body.Status)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.credits) != 0 {
		t.Fatal("must not credit once the budget is gone")
	}
}

func TestLotteryDrawMissDoesNotCredit(t *testing.T) {
	rt := lottery.Runtime{
		Enabled: true,
		Prizes:  []lottery.Prize{{Label: "谢谢参与", Amount: 0, Weight: 1}},
	}
	f := newLotteryFixture(t, rt)

	_, body := f.draw(t, true)
	if body.Status != "miss" {
		t.Fatalf("status = %q, want miss", body.Status)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.credits) != 0 {
		t.Fatal("a miss must not credit balance")
	}
}

func TestLotteryStatusReportsState(t *testing.T) {
	f := newLotteryFixture(t, alwaysWinPool())

	req := httptest.NewRequest(http.MethodGet, "/api/lottery/status", nil)
	req.Header.Set("Authorization", "Bearer "+lotteryUserToken)
	rec := httptest.NewRecorder()
	f.h.LotteryStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d", rec.Code)
	}
	var body lotteryStatusBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Enabled || !body.CanDraw || body.DrawnToday {
		t.Fatalf("unexpected pre-draw status: %+v", body)
	}
	if len(body.Prizes) != 1 || body.Prizes[0].Label != "必中 2 额度" {
		t.Fatalf("prizes not exposed: %+v", body.Prizes)
	}

	if _, drawn := f.draw(t, true); drawn.Status != "win" {
		t.Fatalf("draw failed: %+v", drawn)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/lottery/status", nil)
	req.Header.Set("Authorization", "Bearer "+lotteryUserToken)
	rec = httptest.NewRecorder()
	f.h.LotteryStatus(rec, req)
	body = lotteryStatusBody{}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if !body.DrawnToday || body.CanDraw {
		t.Fatalf("post-draw status wrong: %+v", body)
	}
	if body.TodayPrize != "必中 2 额度" {
		t.Fatalf("today prize = %q", body.TodayPrize)
	}
}

// Weights are an operator secret; the public payload must not leak them.
func TestLotteryStatusHidesWeights(t *testing.T) {
	f := newLotteryFixture(t, alwaysWinPool())
	req := httptest.NewRequest(http.MethodGet, "/api/lottery/status", nil)
	rec := httptest.NewRecorder()
	f.h.LotteryStatus(rec, req)
	if strings.Contains(rec.Body.String(), "weight") {
		t.Fatalf("weights leaked to users: %s", rec.Body.String())
	}
}

func TestAdminLotteryEndpointsRequireAuth(t *testing.T) {
	f := newLotteryFixture(t, alwaysWinPool())

	cases := []struct {
		name string
		fn   func(http.ResponseWriter, *http.Request)
		path string
	}{
		{"settings", f.h.AdminGetLotterySettings, "/api/admin/lottery/settings"},
		{"draws", f.h.AdminLotteryDraws, "/api/admin/lottery/draws"},
		{"stats", f.h.AdminLotteryStats, "/api/admin/lottery/stats"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.fn(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
		})
	}
}

func TestAdminLotterySettingsRoundTrip(t *testing.T) {
	f := newLotteryFixture(t, alwaysWinPool())

	payload := `{"enabled":true,"daily_budget":50,"hard_cap":8,"prizes":[{"label":"A","amount":1,"weight":3},{"label":"B","amount":0,"weight":7}]}`
	req := httptest.NewRequest(http.MethodPut, "/api/admin/lottery/settings", strings.NewReader(payload))
	req.Header.Set("x-api-key", "admin-key")
	rec := httptest.NewRecorder()
	f.h.AdminUpdateLotterySettings(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d body=%s", rec.Code, rec.Body.String())
	}

	got := f.lot.Get()
	if len(got.Prizes) != 2 || got.Prizes[0].Label != "A" || got.Prizes[1].Weight != 7 {
		t.Fatalf("prizes not persisted: %+v", got.Prizes)
	}
	if got.DailyBudget != 50 || got.HardCap != 8 {
		t.Fatalf("budget/cap not persisted: %+v", got)
	}
}

func TestAdminLotterySettingsRejectsZeroWeightPool(t *testing.T) {
	f := newLotteryFixture(t, alwaysWinPool())

	payload := `{"prizes":[{"label":"A","amount":1,"weight":0}]}`
	req := httptest.NewRequest(http.MethodPut, "/api/admin/lottery/settings", strings.NewReader(payload))
	req.Header.Set("x-api-key", "admin-key")
	rec := httptest.NewRecorder()
	f.h.AdminUpdateLotterySettings(rec, req)
	// Normalize falls back to the default pool rather than storing an
	// undrawable one, so the request succeeds with a usable config.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if lottery.TotalWeight(f.lot.Get().Prizes) <= 0 {
		t.Fatal("stored pool must stay drawable")
	}
}

func TestAdminLotteryStatsCountsDraw(t *testing.T) {
	f := newLotteryFixture(t, alwaysWinPool())
	if _, body := f.draw(t, true); body.Status != "win" {
		t.Fatalf("draw failed: %+v", body)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/lottery/stats", nil)
	req.Header.Set("x-api-key", "admin-key")
	rec := httptest.NewRecorder()
	f.h.AdminLotteryStats(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var stats struct {
		TodayDraws  int64   `json:"today_draws"`
		TodayAmount float64 `json:"today_amount"`
		TotalDraws  int64   `json:"total_draws"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &stats); err != nil {
		t.Fatal(err)
	}
	if stats.TodayDraws != 1 || stats.TodayAmount != 2 || stats.TotalDraws != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}
