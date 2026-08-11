package handler_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAdminModuleSettingsToggleGenericModules(t *testing.T) {
	h, _, adminKey := newCampaignAdminHandler(t)

	getRec := httptest.NewRecorder()
	h.AdminModuleSettings(getRec, campaignAdminRequest(http.MethodGet, "/api/admin/modules/settings", adminKey))
	if getRec.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", getRec.Code, getRec.Body.String())
	}
	var before struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &before); err != nil {
		t.Fatal(err)
	}
	if len(before.Items) < 10 {
		t.Fatalf("expected all built-in modules, got %d", len(before.Items))
	}

	putReq := campaignAdminRequest(http.MethodPut, "/api/admin/modules/settings", adminKey)
	putReq.Body = http.NoBody
	putReq = httptest.NewRequest(http.MethodPut, "/api/admin/modules/settings", strings.NewReader(`{"id":"ranking","enabled":false}`))
	putReq.Header.Set("x-api-key", adminKey)
	putRec := httptest.NewRecorder()
	h.AdminModuleSettings(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("put status=%d body=%s", putRec.Code, putRec.Body.String())
	}

	modsRec := httptest.NewRecorder()
	h.Modules(modsRec, httptest.NewRequest(http.MethodGet, "/api/modules", nil))
	if modsRec.Code != http.StatusOK {
		t.Fatalf("modules status=%d body=%s", modsRec.Code, modsRec.Body.String())
	}
	var mods struct {
		Modules []map[string]any `json:"modules"`
	}
	if err := json.Unmarshal(modsRec.Body.Bytes(), &mods); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range mods.Modules {
		if m["id"] == "ranking" {
			found = true
			if m["enabled"].(bool) {
				t.Fatalf("ranking should be disabled in module list: %+v", m)
			}
		}
	}
	if !found {
		t.Fatal("ranking module missing")
	}

	rankRec := httptest.NewRecorder()
	h.RankingRewards(rankRec, httptest.NewRequest(http.MethodGet, "/api/ranking/rewards?range=today", nil))
	if rankRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("ranking rewards status=%d body=%s", rankRec.Code, rankRec.Body.String())
	}
}

func TestAdminModuleSettingsToggleNativeTasks(t *testing.T) {
	h, _, adminKey := newCampaignAdminHandler(t)
	// newCampaignAdminHandler does not attach task settings, so unknown runtime modules should fail safely.
	req := httptest.NewRequest(http.MethodPut, "/api/admin/modules/settings", strings.NewReader(`{"id":"tasks","enabled":false}`))
	req.Header.Set("x-api-key", adminKey)
	rec := httptest.NewRecorder()
	h.AdminModuleSettings(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("tasks without settings should fail safely, status=%d body=%s", rec.Code, rec.Body.String())
	}
}
