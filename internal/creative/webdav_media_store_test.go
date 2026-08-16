package creative

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestWebDAVMediaStorePutOpenDelete(t *testing.T) {
	var mu sync.Mutex
	files := map[string][]byte{}
	dirs := map[string]bool{"/dav/": true}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case "MKCOL":
			dirs[r.URL.Path] = true
			if !strings.HasSuffix(r.URL.Path, "/") {
				dirs[r.URL.Path+"/"] = true
			}
			w.WriteHeader(http.StatusCreated)
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			files[r.URL.Path] = body
			w.WriteHeader(http.StatusCreated)
		case http.MethodGet:
			data, ok := files[r.URL.Path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			if rg := r.Header.Get("Range"); strings.HasPrefix(rg, "bytes=") {
				// minimal bytes=0-0 support
				w.Header().Set("Content-Range", "bytes 0-0/"+strconv.Itoa(len(data)))
				w.Header().Set("Accept-Ranges", "bytes")
				w.WriteHeader(http.StatusPartialContent)
				if len(data) > 0 {
					_, _ = w.Write(data[:1])
				}
				return
			}
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Accept-Ranges", "bytes")
			_, _ = w.Write(data)
		case http.MethodHead:
			data, ok := files[r.URL.Path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.WriteHeader(http.StatusOK)
		case http.MethodDelete:
			if _, ok := files[r.URL.Path]; !ok {
				http.NotFound(w, r)
				return
			}
			delete(files, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method", http.StatusMethodNotAllowed)
		}
	}))
	defer srv.Close()

	store, err := NewWebDAVMediaStore(WebDAVMediaSettings{
		BaseURL:  srv.URL + "/dav/",
		Username: "u",
		Password: "p",
		Root:     "GoogleDrive/sub2api-ext/creative",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	payload := testPNGBytes()
	if err := store.Put(ctx, MediaKindImage, "image-1-1-abcd.png", strings.NewReader(string(payload)), int64(len(payload)), "image/png"); err != nil {
		t.Fatal(err)
	}
	wantPath := path.Join("/dav", "GoogleDrive/sub2api-ext/creative", "images", "image-1-1-abcd.png")
	// httptest path may not match path.Join exactly with mixed; check via open.
	content, err := store.Open(ctx, MediaKindImage, "image-1-1-abcd.png", "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(content.Body)
	_ = content.Body.Close()
	if err != nil || string(got) != string(payload) {
		t.Fatalf("body mismatch pathhint=%s err=%v", wantPath, err)
	}
	size, _, ok, err := store.Stat(ctx, MediaKindImage, "image-1-1-abcd.png")
	if err != nil || !ok || size != int64(len(payload)) {
		t.Fatalf("stat size=%d ok=%v err=%v", size, ok, err)
	}
	if err := store.Delete(ctx, MediaKindImage, "image-1-1-abcd.png"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err := store.Stat(ctx, MediaKindImage, "image-1-1-abcd.png"); err != nil || ok {
		t.Fatalf("deleted still present ok=%v err=%v", ok, err)
	}
}

func TestFallbackMediaStoreReadsLocalWhenRemoteMissing(t *testing.T) {
	root := t.TempDir()
	videoRoot := filepath.Join(root, "creative", "videos")
	local := NewLocalMediaStore(videoRoot)
	ctx := context.Background()
	payload := []byte("hello-video")
	if err := local.Put(ctx, MediaKindVideo, "video-9.mp4", strings.NewReader(string(payload)), int64(len(payload)), "video/mp4"); err != nil {
		t.Fatal(err)
	}

	// remote that always 404
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "MKCOL" {
			w.WriteHeader(http.StatusCreated)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	remote, err := NewWebDAVMediaStore(WebDAVMediaSettings{BaseURL: srv.URL + "/dav/"})
	if err != nil {
		t.Fatal(err)
	}
	store := &fallbackMediaStore{primary: remote, fallback: local}
	content, err := store.Open(ctx, MediaKindVideo, "video-9.mp4", "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(content.Body)
	_ = content.Body.Close()
	if err != nil || string(got) != string(payload) {
		t.Fatalf("fallback body=%q err=%v", got, err)
	}
}
