package handler_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sub2api-ext/internal/config"
	"sub2api-ext/internal/handler"
	"sub2api-ext/internal/notify"
	"sub2api-ext/internal/patrol"
	"sub2api-ext/internal/report"
	"sub2api-ext/internal/settings"
	"sub2api-ext/internal/store"
	"sub2api-ext/internal/sub2api"
)

func newReportHandler(t *testing.T, adminKey string) (*handler.Handler, *report.Service) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "report-h.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := config.Config{}
	cfg.Sub2API.BaseURL = "http://127.0.0.1:1"
	cfg.Sub2API.AdminToken = adminKey
	cfg.Checkin.Timezone = "Asia/Shanghai"
	cfg.Report.SendAt = "09:00"
	cfg.Report.Timezone = "Asia/Shanghai"
	cfg.Report.CoverDay = "yesterday"
	cfg.Notify.Channel = "webhook"
	cfg.Notify.MinLevel = "warn"

	client := sub2api.New(cfg.Sub2API.BaseURL, adminKey, time.Second)
	stg := settings.New(st, cfg.Checkin)
	ps := patrol.NewSettings(st, cfg.Patrol)
	svc := patrol.NewService(client, st, ps)
	h := handler.New(cfg, st, client, stg, svc)

	ns := notify.NewSettings(st, cfg.Notify)
	n := notify.NewNotifier(ns)
	h.SetNotifier(n)

	rs := report.NewSettings(st, cfg.Report)
	r := report.NewService(st, rs, n, report.Deps{
		CheckinBudget:  func() float64 { return stg.Get().DailyBudget },
		LotteryBudget:  func() float64 { return 0 },
		LotteryEnabled: func() bool { return false },
	})
	h.SetReport(r)
	return h, r
}

func TestAdminReportRequiresAuth(t *testing.T) {
	h, _ := newReportHandler(t, "admin-test-key")
	for _, tc := range []struct {
		name string
		fn   func(http.ResponseWriter, *http.Request)
		m    string
	}{
		{"get", h.AdminGetReportSettings, http.MethodGet},
		{"update", h.AdminUpdateReportSettings, http.MethodPut},
		{"preview", h.AdminReportPreview, http.MethodGet},
		{"send", h.AdminReportSend, http.MethodPost},
	} {
		req := httptest.NewRequest(tc.m, "/api/admin/report/"+tc.name, strings.NewReader("{}"))
		rec := httptest.NewRecorder()
		tc.fn(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d, want 401 body=%s", tc.name, rec.Code, rec.Body.String())
		}
	}
}

func TestAdminReportSettingsRoundTrip(t *testing.T) {
	const adminKey = "admin-test-key"
	h, _ := newReportHandler(t, adminKey)

	payload := `{"enabled":true,"send_at":"08:30","timezone":"Asia/Shanghai","cover_day":"today","sections":["checkin","patrol"]}`
	req := httptest.NewRequest(http.MethodPut, "/api/admin/report/settings", strings.NewReader(payload))
	req.Header.Set("x-api-key", adminKey)
	rec := httptest.NewRecorder()
	h.AdminUpdateReportSettings(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/admin/report/settings", nil)
	req.Header.Set("x-api-key", adminKey)
	rec = httptest.NewRecorder()
	h.AdminGetReportSettings(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d body=%s", rec.Code, rec.Body.String())
	}

	var out struct {
		Settings struct {
			Enabled  bool     `json:"enabled"`
			SendAt   string   `json:"send_at"`
			CoverDay string   `json:"cover_day"`
			Sections []string `json:"sections"`
		} `json:"settings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Settings.Enabled || out.Settings.SendAt != "08:30" || out.Settings.CoverDay != "today" {
		t.Fatalf("settings = %+v", out.Settings)
	}
	if len(out.Settings.Sections) != 2 {
		t.Fatalf("sections = %v", out.Settings.Sections)
	}
}

func TestAdminReportPreview(t *testing.T) {
	const adminKey = "admin-test-key"
	h, _ := newReportHandler(t, adminKey)

	// Empty store still produces a titled digest so the admin page can preview
	// the message format before any traffic has arrived.
	req := httptest.NewRequest(http.MethodGet, "/api/admin/report/preview", nil)
	req.Header.Set("x-api-key", adminKey)
	rec := httptest.NewRecorder()
	h.AdminReportPreview(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Message, "运营日报") {
		t.Fatalf("message = %q", out.Message)
	}
}

func TestAdminReportSendFailsWhenNotifyOff(t *testing.T) {
	const adminKey = "admin-test-key"
	h, _ := newReportHandler(t, adminKey)

	req := httptest.NewRequest(http.MethodPost, "/api/admin/report/send", strings.NewReader("{}"))
	req.Header.Set("x-api-key", adminKey)
	rec := httptest.NewRecorder()
	h.AdminReportSend(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "通知") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestAdminReportRejectsEmptySections(t *testing.T) {
	const adminKey = "admin-test-key"
	h, _ := newReportHandler(t, adminKey)
	req := httptest.NewRequest(http.MethodPut, "/api/admin/report/settings",
		strings.NewReader(`{"sections":[]}`))
	req.Header.Set("x-api-key", adminKey)
	rec := httptest.NewRecorder()
	h.AdminUpdateReportSettings(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

