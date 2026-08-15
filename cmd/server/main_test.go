package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestSPAFallbackDoesNotMaskAPINotFound(t *testing.T) {
	handler := spaFallback(http.FileServer(http.FS(testStaticFS())), testStaticFS())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/missing", nil)

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestSPAFallbackDoesNotMaskMissingStaticAsset(t *testing.T) {
	handler := spaFallback(http.FileServer(http.FS(testStaticFS())), testStaticFS())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/assets/missing.js", nil)

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestSPAFallbackServesIndexForFrontendRoute(t *testing.T) {
	handler := spaFallback(http.FileServer(http.FS(testStaticFS())), testStaticFS())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/creative", nil)

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "index page") {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func testStaticFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html": {Data: []byte("index page")},
		"app.js":     {Data: []byte("app")},
	}
}
