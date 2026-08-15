package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientIPIgnoresForwardedHeadersFromUntrustedRemote(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req.RemoteAddr = "198.51.100.23:4321"
	req.Header.Set("CF-Connecting-IP", "203.0.113.99")
	req.Header.Set("X-Forwarded-For", "203.0.113.100")

	if got := clientIP(req); got != "198.51.100.23" {
		t.Fatalf("clientIP=%q", got)
	}
}

func TestClientIPTrustsForwardedHeadersFromProxyRemote(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req.RemoteAddr = "172.18.0.2:4321"
	req.Header.Set("X-Forwarded-For", "203.0.113.100, 172.18.0.1")

	if got := clientIP(req); got != "203.0.113.100" {
		t.Fatalf("clientIP=%q", got)
	}
}
