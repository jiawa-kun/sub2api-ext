package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

type candidate struct {
	jobID       int64
	requestID   string
	assetID     string
	storageKey  string
	contentType string
	size        int64
}

func main() {
	var extDB, grokDB, grokMediaRoot, extMediaRoot string
	var execute bool
	flag.StringVar(&extDB, "ext-db", "", "sub2api-ext SQLite path")
	flag.StringVar(&grokDB, "grok-db", "", "grok2api SQLite path")
	flag.StringVar(&grokMediaRoot, "grok-media-root", "", "grok2api media root")
	flag.StringVar(&extMediaRoot, "ext-media-root", "", "sub2api-ext video archive root")
	flag.BoolVar(&execute, "execute", false, "copy files and update sub2api-ext SQLite")
	flag.Parse()

	if extDB == "" || grokDB == "" || grokMediaRoot == "" || extMediaRoot == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(context.Background(), extDB, grokDB, grokMediaRoot, extMediaRoot, execute); err != nil {
		fmt.Fprintln(os.Stderr, "recovery failed:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, extPath, grokPath, grokMediaRoot, extMediaRoot string, execute bool) error {
	ext, err := sql.Open("sqlite", "file:"+filepath.ToSlash(extPath)+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return err
	}
	defer ext.Close()
	grok, err := sql.Open("sqlite", "file:"+filepath.ToSlash(grokPath)+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(10000)")
	if err != nil {
		return err
	}
	defer grok.Close()

	candidates, err := findCandidates(ctx, ext, grok)
	if err != nil {
		return err
	}
	recoverable, recovered, skipped := 0, 0, 0
	for _, item := range candidates {
		source, pathErr := safeJoin(grokMediaRoot, item.storageKey)
		if pathErr != nil {
			return fmt.Errorf("job %d: %w", item.jobID, pathErr)
		}
		info, statErr := os.Stat(source)
		if statErr != nil || !info.Mode().IsRegular() {
			fmt.Printf("job=%d request=%s action=missing-source\n", item.jobID, item.requestID)
			skipped++
			continue
		}
		if item.size > 0 && info.Size() != item.size {
			return fmt.Errorf("job %d: source size mismatch: db=%d file=%d", item.jobID, item.size, info.Size())
		}
		recoverable++
		name := fmt.Sprintf("video-%d.mp4", item.jobID)
		destination, pathErr := safeJoin(extMediaRoot, name)
		if pathErr != nil {
			return fmt.Errorf("job %d: %w", item.jobID, pathErr)
		}
		if !execute {
			fmt.Printf("job=%d request=%s asset=%s bytes=%d action=would-recover\n", item.jobID, item.requestID, item.assetID, info.Size())
			continue
		}
		if err := os.MkdirAll(extMediaRoot, 0o750); err != nil {
			return err
		}
		if err := copyAtomic(source, destination, info.Size()); err != nil {
			return fmt.Errorf("job %d: %w", item.jobID, err)
		}
		result, err := ext.ExecContext(ctx, `UPDATE creative_jobs SET local_media_file=?,local_media_type=?,local_media_size=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND media_type='video' AND status='completed'`, name, normalizeType(item.contentType), info.Size(), item.jobID)
		if err != nil {
			return fmt.Errorf("job %d update: %w", item.jobID, err)
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return fmt.Errorf("job %d update affected %d rows", item.jobID, n)
		}
		fmt.Printf("job=%d request=%s asset=%s bytes=%d action=recovered\n", item.jobID, item.requestID, item.assetID, info.Size())
		recovered++
	}
	mode := "dry-run"
	if execute {
		mode = "execute"
	}
	fmt.Printf("summary mode=%s candidates=%d recoverable=%d recovered=%d skipped=%d\n", mode, len(candidates), recoverable, recovered, skipped)
	return nil
}

func findCandidates(ctx context.Context, ext, grok *sql.DB) ([]candidate, error) {
	rows, err := ext.QueryContext(ctx, `SELECT id,result_json FROM creative_jobs WHERE media_type='video' AND status='completed' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []candidate
	for rows.Next() {
		var jobID int64
		var resultJSON string
		if err := rows.Scan(&jobID, &resultJSON); err != nil {
			return nil, err
		}
		requestID, err := resultRequestID(resultJSON)
		if err != nil {
			fmt.Printf("job=%d action=skip-invalid-result\n", jobID)
			continue
		}
		var item candidate
		item.jobID, item.requestID = jobID, requestID
		err = grok.QueryRowContext(ctx, `SELECT j.result_asset_id,a.storage_key,a.mime_type,a.size_bytes FROM media_jobs j JOIN media_assets a ON a.id=j.result_asset_id WHERE j.id=? AND j.status='completed' AND a.kind='video'`, requestID).
			Scan(&item.assetID, &item.storageKey, &item.contentType, &item.size)
		if errors.Is(err, sql.ErrNoRows) {
			fmt.Printf("job=%d request=%s action=skip-no-asset\n", jobID, requestID)
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func resultRequestID(raw string) (string, error) {
	var payload struct {
		Video struct {
			URL string `json:"url"`
		} `json:"video"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return "", err
	}
	u, err := url.Parse(strings.TrimSpace(payload.Video.URL))
	if err != nil {
		return "", err
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "videos" && strings.HasPrefix(parts[i+1], "video_") {
			return parts[i+1], nil
		}
	}
	return "", fmt.Errorf("video request id missing")
}

func safeJoin(root, name string) (string, error) {
	root, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", err
	}
	target, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(strings.TrimSpace(name))))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes media root")
	}
	return target, nil
}

func copyAtomic(source, destination string, expectedSize int64) error {
	if info, err := os.Stat(destination); err == nil && info.Mode().IsRegular() && info.Size() == expectedSize {
		return nil
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.CreateTemp(filepath.Dir(destination), ".recover-*.tmp")
	if err != nil {
		return err
	}
	tmp := out.Name()
	committed := false
	defer func() {
		_ = out.Close()
		if !committed {
			_ = os.Remove(tmp)
		}
	}()
	n, err := io.Copy(out, in)
	if err != nil {
		return err
	}
	if n != expectedSize {
		return fmt.Errorf("copied size mismatch: expected=%d actual=%d", expectedSize, n)
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o640); err != nil {
		return err
	}
	if err := os.Rename(tmp, destination); err != nil {
		return err
	}
	committed = true
	return nil
}

func normalizeType(value string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), "video/") {
		return strings.TrimSpace(value)
	}
	return "video/mp4"
}
