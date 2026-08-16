package creative

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"sub2api-ext/internal/store"
)

func TestAuditMissingMediaFindsOrphans(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "media-audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	root := filepath.Join(t.TempDir(), "creative", "videos")
	if err := os.MkdirAll(filepath.Join(filepath.Dir(root), "images"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}

	cfg := NewMediaConfig(st, MediaRuntime{Driver: MediaDriverLocal, LocalVideoRoot: root, LocalFallback: true})
	svc := New(st, nil, nil, "", root)
	if err := svc.SetMediaConfig(cfg); err != nil {
		t.Fatal(err)
	}

	p, err := st.SaveCreativeProvider(ctx, store.CreativeProvider{Name: "p", Kind: "openai_compatible", BaseURL: "http://example.invalid", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	job, err := st.CreateCreativeJob(ctx, store.CreativeJob{
		OrderNo:    "ord-audit-1",
		RequestKey: "rk-audit-1",
		UserID:     9,
		ProviderID: p.ID,
		ModelID:    "m",
		MediaType:  "video",
		Prompt:     "x",
		Status:     store.CreativeJobCompleted,
		Progress:   100,
	})
	if err != nil {
		t.Fatal(err)
	}
	job.LocalMediaFile = "video-missing.mp4"
	job.LocalMediaType = "video/mp4"
	job.LocalMediaSize = 123
	now := time.Now().UTC()
	job.CompletedAt = &now
	if err := st.UpdateCreativeJob(ctx, *job); err != nil {
		t.Fatalf("update job: %v", err)
	}

	imgName := "image-1-1-abcd.jpg"
	imgPath := filepath.Join(filepath.Dir(root), "images", imgName)
	payload := []byte("jpeg-bytes-fake")
	if err := os.WriteFile(imgPath, payload, 0o640); err != nil {
		t.Fatal(err)
	}
	imgJob, err := st.CreateCreativeJob(ctx, store.CreativeJob{
		OrderNo:    "ord-audit-2",
		RequestKey: "rk-audit-2",
		UserID:     9,
		ProviderID: p.ID,
		ModelID:    "m",
		MediaType:  "image",
		Prompt:     "y",
		Status:     store.CreativeJobCompleted,
		Progress:   100,
		ResultJSON: `{"data":[{"local":true}]}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CompleteCreativeImageJob(ctx, *imgJob, []store.CreativeJobImage{{
		JobID: imgJob.ID, ImageIndex: 0, LocalMediaFile: imgName, LocalMediaType: "image/jpeg", LocalMediaSize: int64(len(payload)),
	}}); err != nil {
		t.Fatalf("complete image job: %v", err)
	}

	health, err := svc.CheckMediaHealth(ctx)
	if err != nil || !health.OK {
		t.Fatalf("health=%+v err=%v", health, err)
	}
	report, err := svc.AuditMissingMedia(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.Scanned < 2 {
		t.Fatalf("scanned=%d", report.Scanned)
	}
	if report.Present < 1 {
		t.Fatalf("present=%d report=%+v", report.Present, report)
	}
	if report.Missing < 1 {
		t.Fatalf("expected missing video, report=%+v", report)
	}
	foundMissingVideo := false
	for _, item := range report.MissingItems {
		if item.FileName == "video-missing.mp4" {
			foundMissingVideo = true
		}
	}
	if !foundMissingVideo {
		t.Fatalf("missing video not reported: %+v", report.MissingItems)
	}
	avail, known, _ := svc.MediaAvailability(job.ID, "video", 0, "video-missing.mp4")
	if !known || avail {
		t.Fatalf("availability cache video avail=%v known=%v", avail, known)
	}
}
