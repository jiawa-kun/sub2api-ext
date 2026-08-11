package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"sub2api-ext/internal/config"
	"sub2api-ext/internal/handler"
	"sub2api-ext/internal/settings"
	"sub2api-ext/internal/store"
	"sub2api-ext/internal/sub2api"
)

func newCampaignAdminHandler(t *testing.T) (*handler.Handler, *store.Store, string) {
	t.Helper()
	const adminKey = "campaign-admin-key"
	cfg := config.Default()
	cfg.Store.SQLitePath = filepath.Join(t.TempDir(), "campaign-admin.db")
	cfg.Sub2API.BaseURL = "http://127.0.0.1:1"
	cfg.Sub2API.AdminToken = adminKey
	st, err := store.Open(cfg.Store.SQLitePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	client := sub2api.New(cfg.Sub2API.BaseURL, adminKey, time.Second)
	return handler.New(cfg, st, client, settings.New(st, cfg.Checkin), nil), st, adminKey
}

func campaignAdminRequest(method, target, adminKey string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	req.Header.Set("x-api-key", adminKey)
	return req
}

func TestAdminCampaignPaginationAwardsAndSafeDelete(t *testing.T) {
	h, st, adminKey := newCampaignAdminHandler(t)
	ctx := context.Background()
	emptyID, err := st.CreateRankCampaign(ctx, store.RankCampaign{
		Name: "可删除活动", Board: store.CampaignBoardRewards, StartDate: "2026-08-01", EndDate: "2026-08-31",
		Frequency: store.CampaignFrequencyOnce, Status: store.CampaignStatusDraft, RewardsJSON: `[{"rank":1,"amount":1}]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	awardedID, err := st.CreateRankCampaign(ctx, store.RankCampaign{
		Name: "八月奖励榜", Board: store.CampaignBoardRewards, StartDate: "2026-08-01", EndDate: "2026-08-31",
		Frequency: store.CampaignFrequencyMonthly, Status: store.CampaignStatusActive, RewardsJSON: `[{"rank":1,"amount":5}]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertCampaignAward(ctx, store.RankCampaignAward{CampaignID: awardedID, PeriodKey: "2026-08", UserID: 7, Rank: 1, Amount: 5, Status: "failed", Error: "credit upstream failed"}); err != nil {
		t.Fatal(err)
	}

	listRec := httptest.NewRecorder()
	h.AdminRankCampaigns(listRec, campaignAdminRequest(http.MethodGet, "/api/admin/rank/campaigns?page=1&page_size=10&keyword=八月&status=active", adminKey))
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listRec.Code, listRec.Body.String())
	}
	var list map[string]any
	if err := json.Unmarshal(listRec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if int(list["total"].(float64)) != 1 || len(list["items"].([]any)) != 1 {
		t.Fatalf("list=%v", list)
	}

	awardsRec := httptest.NewRecorder()
	h.AdminRankCampaignByID(awardsRec, campaignAdminRequest(http.MethodGet, "/api/admin/rank/campaigns/"+strconv.FormatInt(awardedID, 10)+"/awards?page=1&page_size=10&status=failed", adminKey))
	if awardsRec.Code != http.StatusOK {
		t.Fatalf("awards status=%d body=%s", awardsRec.Code, awardsRec.Body.String())
	}
	var awards map[string]any
	if err := json.Unmarshal(awardsRec.Body.Bytes(), &awards); err != nil {
		t.Fatal(err)
	}
	item := awards["items"].([]any)[0].(map[string]any)
	if item["error"] != "credit upstream failed" || int(awards["total"].(float64)) != 1 {
		t.Fatalf("awards=%v", awards)
	}

	blockedRec := httptest.NewRecorder()
	h.AdminRankCampaignByID(blockedRec, campaignAdminRequest(http.MethodDelete, "/api/admin/rank/campaigns/"+strconv.FormatInt(awardedID, 10), adminKey))
	if blockedRec.Code != http.StatusConflict {
		t.Fatalf("delete awarded status=%d body=%s", blockedRec.Code, blockedRec.Body.String())
	}

	deleteRec := httptest.NewRecorder()
	h.AdminRankCampaignByID(deleteRec, campaignAdminRequest(http.MethodDelete, "/api/admin/rank/campaigns/"+strconv.FormatInt(emptyID, 10), adminKey))
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete empty status=%d body=%s", deleteRec.Code, deleteRec.Body.String())
	}
	campaign, err := st.GetRankCampaign(ctx, emptyID)
	if err != nil || campaign != nil {
		t.Fatalf("deleted campaign=%+v err=%v", campaign, err)
	}
}
