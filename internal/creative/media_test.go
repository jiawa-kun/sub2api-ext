package creative

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sub2api-ext/internal/store"
)

func TestArchiveImagesKeepsMultipleResultsLocal(t *testing.T) {
	ctx := context.Background()
	payload := testPNGBytes()
	mediaCalls := 0
	media := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/image.png" {
			http.NotFound(w, r)
			return
		}
		mediaCalls++
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(payload)
	}))
	defer media.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "images.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	provider, err := st.SaveCreativeProvider(ctx, store.CreativeProvider{Name: "media", Kind: ProviderOpenAI, BaseURL: media.URL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	job, err := st.CreateCreativeJob(ctx, store.CreativeJob{
		OrderNo: "archive-images", RequestKey: "archive-images", UserID: 9, ProviderID: provider.ID,
		ModelID: "image", MediaType: "image", Status: store.CreativeJobProcessing,
	})
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "creative", "videos")
	svc := New(st, nil, nil, "", root)
	body := []byte(fmt.Sprintf(`{"data":[{"url":%q},{"b64_json":%q},{"url":%q}]}`, media.URL+"/image.png", testPNGBase64, "data:image/png;base64,"+testPNGBase64))
	images, sanitized, err := svc.archiveImages(ctx, job, body)
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 3 || strings.Contains(sanitized, "b64_json") || strings.Contains(sanitized, "data:image") {
		t.Fatalf("images=%+v result=%s", images, sanitized)
	}
	now := time.Now().UTC()
	job.ResultJSON = sanitized
	job.Status = store.CreativeJobCompleted
	job.Progress = 100
	job.CompletedAt = &now
	if err := st.CompleteCreativeImageJob(ctx, *job, images); err != nil {
		t.Fatal(err)
	}
	imageRoot := filepath.Join(filepath.Dir(root), "images")
	paths := make([]string, 0, len(images))
	for _, image := range images {
		path := filepath.Join(imageRoot, image.LocalMediaFile)
		paths = append(paths, path)
		if info, statErr := os.Stat(path); statErr != nil || info.Size() != int64(len(payload)) {
			t.Fatalf("image file=%s info=%+v err=%v", path, info, statErr)
		}
	}

	media.Close()
	for index := range images {
		content, openErr := svc.OpenJobContent(ctx, job, index, "")
		if openErr != nil {
			t.Fatal(openErr)
		}
		got, readErr := io.ReadAll(content.Body)
		_ = content.Body.Close()
		if readErr != nil || string(got) != string(payload) || content.ContentType != "image/png" {
			t.Fatalf("index=%d body=%x type=%q err=%v", index, got, content.ContentType, readErr)
		}
	}
	if mediaCalls != 1 {
		t.Fatalf("remote image calls=%d", mediaCalls)
	}
	if err := svc.DeleteJob(ctx, job.ID, job.UserID); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("deleted image still exists: %s err=%v", path, statErr)
		}
	}
	storedImages, err := st.ListCreativeJobImages(ctx, job.ID)
	if err != nil || len(storedImages) != 0 {
		t.Fatalf("stored images=%+v err=%v", storedImages, err)
	}
}

func TestOpenJobContentArchivesLegacyImageOnFirstRead(t *testing.T) {
	ctx := context.Background()
	payload := testPNGBytes()
	mediaCalls := 0
	media := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mediaCalls++
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(payload)
	}))
	defer media.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	provider, err := st.SaveCreativeProvider(ctx, store.CreativeProvider{Name: "legacy", Kind: ProviderOpenAI, BaseURL: media.URL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	job, err := st.CreateCreativeJob(ctx, store.CreativeJob{
		OrderNo: "legacy-image", RequestKey: "legacy-image", UserID: 9, ProviderID: provider.ID,
		ModelID: "image", MediaType: "image", Status: store.CreativeJobCompleted,
		ResultJSON: `{"data":[{"url":"` + media.URL + `/legacy.png"}]}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := New(st, nil, nil, "", filepath.Join(t.TempDir(), "creative", "videos"))
	content, err := svc.OpenJobContent(ctx, job, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(content.Body)
	_ = content.Body.Close()
	if err != nil || string(got) != string(payload) {
		t.Fatalf("body=%x err=%v", got, err)
	}
	storedImages, err := st.ListCreativeJobImages(ctx, job.ID)
	if err != nil || len(storedImages) != 1 {
		t.Fatalf("stored images=%+v err=%v", storedImages, err)
	}
	saved, err := st.GetCreativeJob(ctx, job.ID, job.UserID)
	if err != nil || !strings.Contains(saved.ResultJSON, `"local":true`) {
		t.Fatalf("saved job=%+v err=%v", saved, err)
	}

	imageRoot := filepath.Join(filepath.Dir(svc.mediaRoot), "images")
	if err := os.WriteFile(filepath.Join(imageRoot, storedImages[0].LocalMediaFile), []byte("truncated"), 0o640); err != nil {
		t.Fatal(err)
	}
	content, err = svc.OpenJobContent(ctx, saved, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err = io.ReadAll(content.Body)
	_ = content.Body.Close()
	if err != nil || string(got) != string(payload) || mediaCalls != 2 {
		t.Fatalf("repaired body=%x calls=%d err=%v", got, mediaCalls, err)
	}

	media.Close()
	content, err = svc.OpenJobContent(ctx, saved, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err = io.ReadAll(content.Body)
	_ = content.Body.Close()
	if err != nil || string(got) != string(payload) || mediaCalls != 2 {
		t.Fatalf("body=%x calls=%d err=%v", got, mediaCalls, err)
	}
}

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
