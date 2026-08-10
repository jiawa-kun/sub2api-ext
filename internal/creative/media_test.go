package creative

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"sub2api-ext/internal/store"
)

func TestArchiveVideoAndOpenLocalContent(t *testing.T) {
	payload := []byte("fake-mp4-video-content")
	media := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/video.mp4" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Length", "22")
		_, _ = w.Write(payload)
	}))
	defer media.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	provider, err := st.SaveCreativeProvider(context.Background(), store.CreativeProvider{Name: "media", Kind: ProviderOpenAI, BaseURL: media.URL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "videos")
	svc := New(st, nil, nil, "", root)
	job := &store.CreativeJob{
		ID: 42, ProviderID: provider.ID, MediaType: "video", Status: store.CreativeJobProcessing,
		ResultJSON: `{"video":{"url":"` + media.URL + `/video.mp4"}}`,
	}
	if err := svc.archiveVideo(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if job.LocalMediaFile != "video-42.mp4" || job.LocalMediaSize != int64(len(payload)) {
		t.Fatalf("archive metadata=%+v", job)
	}
	if _, err := os.Stat(filepath.Join(root, job.LocalMediaFile)); err != nil {
		t.Fatal(err)
	}

	media.Close()
	job.Status = store.CreativeJobCompleted
	content, err := svc.OpenJobContent(context.Background(), job, 0, "bytes=0-3")
	if err != nil {
		t.Fatal(err)
	}
	defer content.Body.Close()
	if content.ReadSeeker == nil || content.AcceptRanges != "bytes" || content.ContentType != "video/mp4" {
		t.Fatalf("local content=%+v", content)
	}
	got, err := io.ReadAll(content.Body)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("body=%q err=%v", got, err)
	}
}

func TestLocalMediaPathRequiresConfiguredRoot(t *testing.T) {
	svc := &Service{}
	if _, err := svc.localMediaPath("video-1.mp4"); err == nil {
		t.Fatal("empty media root accepted")
	}
}

func TestDeleteJobRemovesArchivedVideo(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "delete.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	provider, err := st.SaveCreativeProvider(ctx, store.CreativeProvider{Name: "media", Kind: ProviderOpenAI, BaseURL: "https://provider.example.com", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "videos")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}
	job, err := st.CreateCreativeJob(ctx, store.CreativeJob{OrderNo: "delete-video", RequestKey: "delete-video", UserID: 9, ProviderID: provider.ID, ModelID: "video", MediaType: "video", Status: store.CreativeJobCompleted})
	if err != nil {
		t.Fatal(err)
	}
	job.LocalMediaFile = fmt.Sprintf("video-%d.mp4", job.ID)
	job.LocalMediaType = "video/mp4"
	job.LocalMediaSize = 5
	if err := os.WriteFile(filepath.Join(root, job.LocalMediaFile), []byte("video"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateCreativeJob(ctx, *job); err != nil {
		t.Fatal(err)
	}
	svc := New(st, nil, nil, "", root)
	if err := svc.DeleteJob(ctx, job.ID, job.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, job.LocalMediaFile)); !os.IsNotExist(err) {
		t.Fatalf("archived video still exists: %v", err)
	}
}
