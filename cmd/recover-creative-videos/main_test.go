package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestResultRequestID(t *testing.T) {
	id, err := resultRequestID(`{"video":{"url":"/v1/videos/video_abc-123/content"}}`)
	if err != nil || id != "video_abc-123" {
		t.Fatalf("id=%q err=%v", id, err)
	}
}

func TestSafeJoinRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	if _, err := safeJoin(root, "../outside.mp4"); err == nil {
		t.Fatal("path traversal accepted")
	}
	path, err := safeJoin(root, "videos/vi/file.mp4")
	if err != nil || filepath.Dir(filepath.Dir(path)) != filepath.Join(root, "videos") {
		t.Fatalf("path=%q err=%v", path, err)
	}
}

func TestRunDryRunThenExecute(t *testing.T) {
	root := t.TempDir()
	extDB := filepath.Join(root, "ext.db")
	grokDB := filepath.Join(root, "grok.db")
	grokMedia := filepath.Join(root, "grok-media")
	extMedia := filepath.Join(root, "ext-media")

	ext := openTestDB(t, extDB)
	mustExec(t, ext, `CREATE TABLE creative_jobs (id INTEGER PRIMARY KEY, media_type TEXT NOT NULL, status TEXT NOT NULL, result_json TEXT NOT NULL, local_media_file TEXT NOT NULL DEFAULT '', local_media_type TEXT NOT NULL DEFAULT '', local_media_size INTEGER NOT NULL DEFAULT 0, updated_at TEXT NOT NULL)`)
	mustExec(t, ext, `INSERT INTO creative_jobs(id,media_type,status,result_json,updated_at) VALUES(7,'video','completed','{"video":{"url":"/v1/videos/video_test/content"}}','2026-08-10T00:00:00Z')`)
	if err := ext.Close(); err != nil {
		t.Fatal(err)
	}

	grok := openTestDB(t, grokDB)
	mustExec(t, grok, `CREATE TABLE media_jobs (id TEXT PRIMARY KEY, result_asset_id TEXT NOT NULL, status TEXT NOT NULL)`)
	mustExec(t, grok, `CREATE TABLE media_assets (id TEXT PRIMARY KEY, storage_key TEXT NOT NULL, mime_type TEXT NOT NULL, size_bytes INTEGER NOT NULL, kind TEXT NOT NULL)`)
	mustExec(t, grok, `INSERT INTO media_jobs(id,result_asset_id,status) VALUES('video_test','asset_test','completed')`)
	payload := []byte("test mp4 payload")
	mustExec(t, grok, `INSERT INTO media_assets(id,storage_key,mime_type,size_bytes,kind) VALUES('asset_test','videos/vi/test.mp4','video/mp4',?,'video')`, len(payload))
	if err := grok.Close(); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(grokMedia, "videos", "vi", "test.mp4")
	if err := os.MkdirAll(filepath.Dir(source), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, payload, 0o640); err != nil {
		t.Fatal(err)
	}

	if err := run(context.Background(), extDB, grokDB, grokMedia, extMedia, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(extMedia, "video-7.mp4")); !os.IsNotExist(err) {
		t.Fatalf("dry-run created destination: %v", err)
	}
	assertLocalMedia(t, extDB, "", "", 0)

	if err := run(context.Background(), extDB, grokDB, grokMedia, extMedia, true); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), extDB, grokDB, grokMedia, extMedia, true); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(extMedia, "video-7.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("destination payload = %q", got)
	}
	assertLocalMedia(t, extDB, "video-7.mp4", "video/mp4", int64(len(payload)))
}

func openTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func assertLocalMedia(t *testing.T, path, wantFile, wantType string, wantSize int64) {
	t.Helper()
	db := openTestDB(t, path)
	defer db.Close()
	var gotFile, gotType string
	var gotSize int64
	if err := db.QueryRow(`SELECT local_media_file,local_media_type,local_media_size FROM creative_jobs WHERE id=7`).Scan(&gotFile, &gotType, &gotSize); err != nil {
		t.Fatal(err)
	}
	if gotFile != wantFile || gotType != wantType || gotSize != wantSize {
		t.Fatalf("local media = (%q, %q, %d), want (%q, %q, %d)", gotFile, gotType, gotSize, wantFile, wantType, wantSize)
	}
}
