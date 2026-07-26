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
	"sub2api-ext/internal/notify"
	"sub2api-ext/internal/patrol"
	"sub2api-ext/internal/settings"
	"sub2api-ext/internal/store"
	"sub2api-ext/internal/sub2api"
)

func newNotifyHandler(t *testing.T, adminKey string) (*handler.Handler, *notify.Notifier) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := config.Config{}
	cfg.Sub2API.BaseURL = "http://127.0.0.1:1"
	cfg.Sub2API.AdminToken = adminKey
	cfg.Checkin.Timezone = "Asia/Shanghai"
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
	return h, n
}

// The admin read endpoint must never return the raw webhook URL or secret.
func TestAdminNotifySettingsMasksCredentials(t *testing.T) {
	const adminKey = "admin-test-key"
	h, n := newNotifyHandler(t, adminKey)

	const secretURL = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=TOP-SECRET-123"
	const secretTok = "super-secret-token-value"
	enabled := true
	target := secretURL
	secret := secretTok
	if _, err := n.Settings().Update(context.Background(), notify.UpdateInput{
		Enabled: &enabled, Target: &target, Secret: &secret,
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/notify/settings", nil)
	req.Header.Set("x-api-key", adminKey)
	rec := httptest.NewRecorder()
	h.AdminGetNotifySettings(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	if strings.Contains(body, "TOP-SECRET-123") {
		t.Fatalf("response leaked the webhook key: %s", body)
	}
	if strings.Contains(body, secretTok) {
		t.Fatalf("response leaked the secret: %s", body)
	}

	var out struct {
		Settings struct {
			TargetConfigured bool   `json:"target_configured"`
			SecretConfigured bool   `json:"secret_configured"`
			TargetMasked     string `json:"target_masked"`
		} `json:"settings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Settings.TargetConfigured || !out.Settings.SecretConfigured {
		t.Fatalf("configured flags = %+v", out.Settings)
	}
	if out.Settings.TargetMasked == "" {
		t.Fatal("masked target should be present")
	}
}

func TestAdminNotifyRequiresAuth(t *testing.T) {
	h, _ := newNotifyHandler(t, "admin-test-key")

	for _, tc := range []struct {
		name string
		fn   func(http.ResponseWriter, *http.Request)
		m    string
	}{
		{"get", h.AdminGetNotifySettings, http.MethodGet},
		{"update", h.AdminUpdateNotifySettings, http.MethodPut},
		{"test", h.AdminNotifyTest, http.MethodPost},
	} {
		req := httptest.NewRequest(tc.m, "/api/admin/notify/settings", strings.NewReader("{}"))
		rec := httptest.NewRecorder()
		tc.fn(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d, want 401", tc.name, rec.Code)
		}
	}
}

// Omitting target/secret must preserve the stored values rather than wipe them.
func TestAdminNotifyUpdateKeepsStoredSecrets(t *testing.T) {
	const adminKey = "admin-test-key"
	h, n := newNotifyHandler(t, adminKey)

	const url = "https://example.com/hook"
	enabled := true
	target := url
	secret := "keep-me"
	if _, err := n.Settings().Update(context.Background(), notify.UpdateInput{
		Enabled: &enabled, Target: &target, Secret: &secret,
	}); err != nil {
		t.Fatal(err)
	}

	// update only the level; no target/secret in the payload
	req := httptest.NewRequest(http.MethodPut, "/api/admin/notify/settings",
		strings.NewReader(`{"min_level":"error"}`))
	req.Header.Set("x-api-key", adminKey)
	rec := httptest.NewRecorder()
	h.AdminUpdateNotifySettings(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}

	rt := n.Settings().Get()
	if rt.Target != url {
		t.Fatalf("target was clobbered: %q", rt.Target)
	}
	if rt.Secret != "keep-me" {
		t.Fatalf("secret was clobbered: %q", rt.Secret)
	}
	if rt.MinLevel != "error" {
		t.Fatalf("min_level = %q", rt.MinLevel)
	}
}
