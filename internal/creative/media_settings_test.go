package creative

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"sub2api-ext/internal/store"
)

func TestMediaConfigUpdateLocalAndPersist(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "media-settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	root := filepath.Join(t.TempDir(), "creative", "videos")
	cfg := NewMediaConfig(st, MediaRuntime{Driver: MediaDriverLocal, LocalVideoRoot: root, LocalFallback: true})
	svc := New(st, nil, nil, "", root)
	if err := svc.SetMediaConfig(cfg); err != nil {
		t.Fatal(err)
	}
	driver := MediaDriverLocal
	fb := true
	rt, err := svc.UpdateMediaConfig(ctx, MediaUpdateInput{Driver: &driver, LocalFallback: &fb})
	if err != nil {
		t.Fatal(err)
	}
	if rt.Driver != MediaDriverLocal {
		t.Fatalf("driver=%s", rt.Driver)
	}
	// reload from sqlite
	cfg2 := NewMediaConfig(st, MediaRuntime{Driver: MediaDriverWebDAV, LocalVideoRoot: root, LocalFallback: false})
	got := cfg2.Get()
	if got.Driver != MediaDriverLocal || !got.LocalFallback {
		t.Fatalf("reloaded=%+v", got)
	}
}

func TestMediaConfigRejectsBrokenWebDAV(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "media-settings-webdav.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	root := filepath.Join(t.TempDir(), "creative", "videos")
	cfg := NewMediaConfig(st, MediaRuntime{Driver: MediaDriverLocal, LocalVideoRoot: root, LocalFallback: true})
	svc := New(st, nil, nil, "", root)
	if err := svc.SetMediaConfig(cfg); err != nil {
		t.Fatal(err)
	}
	driver := MediaDriverWebDAV
	url := "http://127.0.0.1:1/dav/"
	_, err = svc.UpdateMediaConfig(ctx, MediaUpdateInput{Driver: &driver, WebDAVURL: &url})
	if err == nil || !strings.Contains(err.Error(), "连通性") {
		t.Fatalf("expected connectivity failure, got %v", err)
	}
	if svc.MediaDriver() != MediaDriverLocal {
		t.Fatalf("driver changed on failed update: %s", svc.MediaDriver())
	}
}

func TestProbeLocalMediaRuntime(t *testing.T) {
	root := filepath.Join(t.TempDir(), "creative", "videos")
	err := ProbeMediaRuntime(context.Background(), MediaRuntime{Driver: MediaDriverLocal, LocalVideoRoot: root})
	if err != nil {
		t.Fatal(err)
	}
}
