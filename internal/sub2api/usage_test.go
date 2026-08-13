package sub2api_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sub2api-ext/internal/sub2api"
)

func TestFetchTokenUsageRankingSortsCompleteSet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/admin/dashboard/users-ranking" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("limit"); got != "5000" {
			t.Fatalf("limit=%s want 5000", got)
		}
		if got := r.Header.Get("x-api-key"); got != "admin-key" {
			t.Fatalf("x-api-key=%q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"code":0,"data":{"total_actual_cost":27,"total_requests":11,"total_tokens":500,"ranking":[`+
			`{"user_id":1,"username":"one","actual_cost":10,"requests":1,"tokens":100},`+
			`{"user_id":2,"username":"two","actual_cost":8,"requests":2,"tokens":200},`+
			`{"user_id":3,"username":"three","actual_cost":7,"requests":4,"tokens":100},`+
			`{"user_id":4,"username":"four","actual_cost":2,"requests":4,"tokens":100}]}}`)
	}))
	defer server.Close()

	client := sub2api.New(server.URL, "admin-key", time.Second)
	result, err := client.FetchTokenUsageRanking(context.Background(), sub2api.UsageRankQuery{
		FromDate: "2026-08-12", ToDate: "2026-08-12", Limit: 3, UserID: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 3 || result.UserCount != 4 {
		t.Fatalf("items=%d user_count=%d", len(result.Items), result.UserCount)
	}
	wantIDs := []int64{2, 1, 3}
	for i, want := range wantIDs {
		if result.Items[i].UserID != want || result.Items[i].Rank != i+1 {
			t.Fatalf("item[%d]=%+v want user=%d rank=%d", i, result.Items[i], want, i+1)
		}
	}
	if result.MyRank != 4 || result.MyTokens != 100 || result.MyRequestCount != 4 {
		t.Fatalf("my metrics=%+v", result)
	}
	if result.TopTokens != 200 || result.Top3TokenShare != 0.8 {
		t.Fatalf("top metrics=%+v", result)
	}
	if result.Items[0].TokenShare != 0.4 || result.Items[0].AvgTokensPerRequest != 100 {
		t.Fatalf("derived metrics=%+v", result.Items[0])
	}
}

func TestFetchTokenUsageRankingRejectsIncompleteTotals(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"code":0,"data":{"total_tokens":1000,"ranking":[{"user_id":1,"tokens":100}]}}`)
	}))
	defer server.Close()

	client := sub2api.New(server.URL, "admin-key", time.Second)
	_, err := client.FetchTokenUsageRanking(context.Background(), sub2api.UsageRankQuery{FromDate: "2026-08-12", ToDate: "2026-08-12", Limit: 20})
	if err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("err=%v want incomplete ranking", err)
	}
}

func TestResolveRange(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 30, 15, 0, 0, 0, loc)
	from, to, p := sub2api.ResolveRange("today", now, loc)
	if from != "2026-07-30" || to != "2026-07-30" || p != "today" {
		t.Fatalf("today %s %s %s", from, to, p)
	}
	from, to, p = sub2api.ResolveRange("yesterday", now, loc)
	if from != "2026-07-29" || to != "2026-07-29" || p != "yesterday" {
		t.Fatalf("yesterday %s %s %s", from, to, p)
	}
	from, to, p = sub2api.ResolveRange("7d", now, loc)
	if from != "2026-07-24" || to != "2026-07-30" || p != "7d" {
		t.Fatalf("7d %s %s %s", from, to, p)
	}
	from, to, p = sub2api.ResolveRange("30d", now, loc)
	if from != "2026-07-01" || to != "2026-07-30" || p != "30d" {
		t.Fatalf("30d %s %s %s", from, to, p)
	}
}
