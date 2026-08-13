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
	if got := maskDisplayName("a"); got != "***" {
		t.Fatalf("single=%q", got)
	}
	if got := maskDisplayName("ab"); got != "a***b" {
		t.Fatalf("two=%q", got)
	}
	if got := maskDisplayName("abcd"); got != "a***d" {
		t.Fatalf("mid=%q", got)
	}
	if got := maskDisplayName("abcdef"); got != "ab***ef" {
		t.Fatalf("long=%q", got)
	}
	if got := maskDisplayName("***"); got != "***" {
		t.Fatalf("stars=%q", got)
	}
}

func TestMaskDisplayNameWithUserID(t *testing.T) {
	if got := maskDisplayNameWithUserID("***", 88); got != "用***8" {
		t.Fatalf("useless with id=%q", got)
	}
	if got := maskDisplayNameWithUserID("ab", 0); got != "a***b" {
		t.Fatalf("two no id=%q", got)
	}
	if got := maskDisplayNameWithUserID("jiawa", 9); got != "ji***wa" {
		t.Fatalf("normal=%q", got)
	}
	if got := maskDisplayNameWithUserID("", 3); got != "用***3" {
		t.Fatalf("empty id=%q", got)
	}
}

func TestMaskEmail(t *testing.T) {
	cases := map[string]string{
		"":                    "",
		"a@x.com":             "***@x.com",
		"ab@x.com":            "a***b@x.com",
		"abcd@x.com":          "a***d@x.com",
		"abcdef@gmail.com":    "ab***ef@gmail.com",
		"yangwu@example.com":  "ya***wu@example.com",
	}
	for in, want := range cases {
		if got := maskEmail(in); got != want {
			t.Fatalf("maskEmail(%q)=%q want %q", in, got, want)
		}
	}
}

func TestResolveRankIdentitiesIncludesMaskedEmail(t *testing.T) {
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
	me := &sub2api.User{ID: 1, Username: "meuser", Email: "me@ex.com"}
	ctx := context.Background()

	idents := h.resolveRankIdentities(ctx, []int64{1, 2, 3}, me)
	if idents[1].DisplayAccount != "m***e@ex.com" {
		t.Fatalf("self account=%q", idents[1].DisplayAccount)
	}
	if idents[2].DisplayName == "" || idents[2].DisplayAccount == "" {
		t.Fatalf("id2=%+v", idents[2])
	}
	if idents[2].DisplayAccount != "u***2@ex.com" {
		t.Fatalf("id2 account=%q", idents[2].DisplayAccount)
	}
	if hits.Load() != 2 {
		t.Fatalf("hits=%d", hits.Load())
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
	me := &sub2api.User{ID: 1, Username: "meuser"}
	ctx := context.Background()

	names1 := h.resolveRankDisplayNames(ctx, []int64{1, 2, 2, 3}, me)
	if names1[1] == "" || names1[2] == "" || names1[3] == "" {
		t.Fatalf("names=%v", names1)
	}
	firstHits := hits.Load()
	if firstHits != 2 {
		t.Fatalf("expected exactly 2 admin fetches for ids 2/3, hits=%d names=%v", firstHits, names1)
	}

	_ = h.resolveRankDisplayNames(ctx, []int64{2, 3}, me)
	if hits.Load() != firstHits {
		t.Fatalf("cache miss caused extra hits: before=%d after=%d", firstHits, hits.Load())
	}
}
