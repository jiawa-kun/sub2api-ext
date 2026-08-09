package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"sub2api-ext/internal/config"
	"sub2api-ext/internal/creative"
	"sub2api-ext/internal/settings"
	"sub2api-ext/internal/store"
	"sub2api-ext/internal/sub2api"
)

func TestCreativeProviderPublicRedactsAPIKey(t *testing.T) {
	out := providerPublic(store.CreativeProvider{ID: 1, Name: "media", APIKey: "abcd-secret-wxyz"})
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "abcd-secret-wxyz") || strings.Contains(text, "secret") {
		t.Fatalf("provider API leaked key: %s", text)
	}
	if out["api_key_configured"] != true || out["api_key_masked"] != "abcd****wxyz" {
		t.Fatalf("provider key metadata unexpected: %+v", out)
	}
}

func TestCreativeJobPublicDoesNotExposeUpstreamResult(t *testing.T) {
	out := creativeJobPublic(&store.CreativeJob{
		ID: 1, UserID: 7, MediaType: "image", ResultJSON: `{"data":[{"url":"https://upstream.example/secret.png"}]}`,
	})
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "upstream.example") || strings.Contains(text, "result_json") {
		t.Fatalf("public creative job leaked upstream result: %s", text)
	}
	if !strings.Contains(text, `"content_count":1`) {
		t.Fatalf("public creative job missing content count: %s", text)
	}
}

func TestCreativeCredentialAPINeverReturnsPlaintext(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/me":
			_, _ = io.WriteString(w, `{"data":{"id":7,"username":"tester","role":"user","balance":10}}`)
		case "/api/v1/admin/accounts":
			_, _ = io.WriteString(w, `{"code":0,"data":{"items":[{"id":1,"schedulable":true,"status":"active","credentials":{"model_mapping":{"grok-imagine-image":"image"}}}],"page":1,"pages":1,"total":1}}`)
		case "/v1/models":
			if r.Header.Get("Authorization") != "Bearer user-media-key" {
				t.Errorf("model auth=%q", r.Header.Get("Authorization"))
			}
			_, _ = io.WriteString(w, `{"data":[{"id":"grok-imagine-image"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	st, err := store.Open(filepath.Join(t.TempDir(), "creative-api.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, err := st.SaveCreativeProvider(context.Background(), store.CreativeProvider{Name: "Sub2API 账号池", Kind: creative.ProviderPool, BaseURL: upstream.URL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Sub2API.BaseURL = upstream.URL
	cfg.Sub2API.AdminToken = "admin-key"
	client := sub2api.New(upstream.URL, "admin-key", time.Second)
	svc := creative.New(st, client, nil, "test-credential-secret-with-at-least-32-characters")
	h := New(cfg, st, client, settings.New(st, cfg.Checkin), nil)
	h.SetCreative(svc)

	request := httptest.NewRequest(http.MethodPost, "/api/creative/credentials", strings.NewReader(`{"provider_id":`+strconv.FormatInt(p.ID, 10)+`,"api_key":"user-media-key"}`))
	request.Header.Set("Authorization", "Bearer login-token")
	recorder := httptest.NewRecorder()
	h.CreativeCredentials(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	assertCredentialResponseRedacted(t, recorder.Body.String())

	request = httptest.NewRequest(http.MethodGet, "/api/creative/credentials", nil)
	request.Header.Set("Authorization", "Bearer login-token")
	recorder = httptest.NewRecorder()
	h.CreativeCredentials(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	assertCredentialResponseRedacted(t, recorder.Body.String())
}

func assertCredentialResponseRedacted(t *testing.T, body string) {
	t.Helper()
	if strings.Contains(body, "user-media-key") || strings.Contains(body, "api_key_cipher") || strings.Contains(body, "v1:") {
		t.Fatalf("credential response leaked secret: %s", body)
	}
	if !strings.Contains(body, `"key_hint":"****-key"`) {
		t.Fatalf("credential response missing masked hint: %s", body)
	}
}
