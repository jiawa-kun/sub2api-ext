package handler_test

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sub2api-ext/internal/config"
	"sub2api-ext/internal/handler"
	"sub2api-ext/internal/settings"
	"sub2api-ext/internal/store"
	"sub2api-ext/internal/sub2api"
)

func newTutorialHandler(t *testing.T, adminKey string) *handler.Handler {
	t.Helper()
	cfg := config.Default()
	cfg.Store.SQLitePath = filepath.Join(t.TempDir(), "tutorial.db")
	cfg.Sub2API.BaseURL = "http://127.0.0.1:1"
	cfg.Sub2API.AdminToken = adminKey

	st, err := store.Open(cfg.Store.SQLitePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	client := sub2api.New(cfg.Sub2API.BaseURL, adminKey, time.Second)
	return handler.New(cfg, st, client, settings.New(st, cfg.Checkin), nil)
}

func tutorialUploadRequest(t *testing.T, filename string, content []byte, adminKey string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/admin/pages/tutorial/assets", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	if adminKey != "" {
		req.Header.Set("x-api-key", adminKey)
	}
	return req
}

func TestTutorialAssetUploadAndPublicRead(t *testing.T) {
	const adminKey = "tutorial-admin-key"
	h := newTutorialHandler(t, adminKey)
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 32)...)

	uploadRec := httptest.NewRecorder()
	h.AdminUploadTutorialAsset(uploadRec, tutorialUploadRequest(t, "guide.png", png, adminKey))
	if uploadRec.Code != http.StatusCreated {
		t.Fatalf("upload status=%d body=%s", uploadRec.Code, uploadRec.Body.String())
	}
	var uploaded struct {
		Filename string `json:"filename"`
		URL      string `json:"url"`
	}
	if err := json.Unmarshal(uploadRec.Body.Bytes(), &uploaded); err != nil {
		t.Fatal(err)
	}
	if uploaded.Filename == "" || uploaded.URL != "./tutorial-assets/"+uploaded.Filename {
		t.Fatalf("unexpected upload response: %+v", uploaded)
	}

	readReq := httptest.NewRequest(http.MethodGet, "/tutorial-assets/"+uploaded.Filename, nil)
	readRec := httptest.NewRecorder()
	h.PublicTutorialAsset(readRec, readReq)
	if readRec.Code != http.StatusOK {
		t.Fatalf("read status=%d body=%s", readRec.Code, readRec.Body.String())
	}
	if got := readRec.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("content type=%q", got)
	}
	if got := readRec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("nosniff header=%q", got)
	}
	if got := readRec.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Fatalf("cache header=%q", got)
	}
	if !bytes.Equal(readRec.Body.Bytes(), png) {
		t.Fatal("served image differs from upload")
	}
}

func TestTutorialAssetUploadRequiresAdminAndImage(t *testing.T) {
	const adminKey = "tutorial-admin-key"
	h := newTutorialHandler(t, adminKey)
	png := []byte("\x89PNG\r\n\x1a\n")

	unauthorized := httptest.NewRecorder()
	h.AdminUploadTutorialAsset(unauthorized, tutorialUploadRequest(t, "guide.png", png, ""))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}

	invalid := httptest.NewRecorder()
	h.AdminUploadTutorialAsset(invalid, tutorialUploadRequest(t, "guide.txt", []byte("not an image"), adminKey))
	if invalid.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("invalid type status=%d body=%s", invalid.Code, invalid.Body.String())
	}

	traversal := httptest.NewRecorder()
	h.PublicTutorialAsset(traversal, httptest.NewRequest(http.MethodGet, "/tutorial-assets/../secret.png", nil))
	if traversal.Code != http.StatusNotFound {
		t.Fatalf("traversal status=%d", traversal.Code)
	}
}

func TestTutorialAssetUploadRejectsOversize(t *testing.T) {
	const adminKey = "tutorial-admin-key"
	h := newTutorialHandler(t, adminKey)
	content := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, (5<<20)+1)...)
	rec := httptest.NewRecorder()
	h.AdminUploadTutorialAsset(rec, tutorialUploadRequest(t, "large.png", content, adminKey))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize status=%d body=%s", rec.Code, rec.Body.String())
	}
}
