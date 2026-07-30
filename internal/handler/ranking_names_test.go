package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sub2api-ext/internal/sub2api"
)

func TestMaskDisplayName(t *testing.T) {
	if got := maskDisplayName("ab"); got != "***" {
		t.Fatalf("short=%q", got)
	}
	if got := maskDisplayName("abcd"); got != "a***d" {
		t.Fatalf("mid=%q", got)
	}
	if got := maskDisplayName("abcdef"); got != "ab***ef" {
		t.Fatalf("long=%q", got)
	}
}

func TestResolveRankDisplayNamesCache(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if !strings.Contains(r.URL.Path, "/api/v1/admin/users/") {
			http.NotFound(w, r)
			return
		}
		idStr := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		fmt.Fprintf(w, `{"code":0,"data":{"id":%s,"username":"user%s","email":"u%s@ex.com"}}`, idStr, idStr, idStr)
	}))
	defer srv.Close()

	client := sub2api.New(srv.URL, "test-admin-key", time.Second)
	h := &Handler{client: client}
	// effectiveAdminCred empty + settings nil => syncAdminCred keeps client token
	me := &sub2api.User{ID: 1, Username: "meuser"}
	ctx := context.Background()

	names1 := h.resolveRankDisplayNames(ctx, []int64{1, 2, 2, 3}, me)
	if names1[1] == "" || names1[2] == "" || names1[3] == "" {
		t.Fatalf("names=%v", names1)
	}
	// self should not hit admin
	firstHits := hits.Load()
	if firstHits != 2 {
		t.Fatalf("expected exactly 2 admin fetches for ids 2/3, hits=%d names=%v", firstHits, names1)
	}

	_ = h.resolveRankDisplayNames(ctx, []int64{2, 3}, me)
	if hits.Load() != firstHits {
		t.Fatalf("cache miss caused extra hits: before=%d after=%d", firstHits, hits.Load())
	}
}
