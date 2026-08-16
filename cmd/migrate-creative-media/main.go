package main

import (
	"context"
	"database/sql"
	"flag"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sub2api-ext/internal/creative"

	_ "modernc.org/sqlite"
)

func main() {
	sqlitePath := flag.String("sqlite", envOr("SQLITE_PATH", "./data/checkin.db"), "sqlite db path")
	localRoot := flag.String("local-root", "", "local creative/videos directory; default derives from sqlite")
	webdavURL := flag.String("webdav-url", envOr("CREATIVE_WEBDAV_URL", ""), "webdav base url, e.g. https://alist.example/dav/")
	webdavUser := flag.String("webdav-username", envOr("CREATIVE_WEBDAV_USERNAME", ""), "webdav username")
	webdavPass := flag.String("webdav-password", envOr("CREATIVE_WEBDAV_PASSWORD", ""), "webdav password")
	webdavRoot := flag.String("webdav-root", envOr("CREATIVE_WEBDAV_ROOT", ""), "optional path under webdav base")
	dryRun := flag.Bool("dry-run", false, "only report actions")
	flag.Parse()

	if strings.TrimSpace(*webdavURL) == "" {
		log.Fatal("webdav-url is required")
	}
	videoRoot := strings.TrimSpace(*localRoot)
	if videoRoot == "" {
		videoRoot = filepath.Join(filepath.Dir(*sqlitePath), "creative", "videos")
	}
	imageRoot := filepath.Join(filepath.Dir(videoRoot), "images")

	remote, err := creative.NewWebDAVMediaStore(creative.WebDAVMediaSettings{
		BaseURL:  *webdavURL,
		Username: *webdavUser,
		Password: *webdavPass,
		Root:     *webdavRoot,
	})
	if err != nil {
		log.Fatalf("webdav: %v", err)
	}
		db, err := sql.Open("sqlite", *sqlitePath)
	if err != nil {
		log.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	ctx := context.Background()
	type item struct {
		kind string
		name string
		size int64
	}
	items := make([]item, 0, 128)

	rows, err := db.QueryContext(ctx, `SELECT local_media_file, local_media_size FROM creative_jobs WHERE IFNULL(deleted_at,'')='' AND media_type='video' AND IFNULL(local_media_file,'')!=''`)
	if err != nil {
		log.Fatalf("query videos: %v", err)
	}
	for rows.Next() {
		var name string
		var size int64
		if err := rows.Scan(&name, &size); err != nil {
			log.Fatalf("scan video: %v", err)
		}
		items = append(items, item{kind: creative.MediaKindVideo, name: name, size: size})
	}
	rows.Close()

	rows, err = db.QueryContext(ctx, `SELECT local_media_file, local_media_size FROM creative_job_images WHERE IFNULL(local_media_file,'')!=''`)
	if err != nil {
		log.Fatalf("query images: %v", err)
	}
	for rows.Next() {
		var name string
		var size int64
		if err := rows.Scan(&name, &size); err != nil {
			log.Fatalf("scan image: %v", err)
		}
		items = append(items, item{kind: creative.MediaKindImage, name: name, size: size})
	}
	rows.Close()

	var uploaded, skipped, missing, failed int
	for _, it := range items {
		localPath := filepath.Join(imageRoot, it.name)
		if it.kind == creative.MediaKindVideo {
			localPath = filepath.Join(videoRoot, it.name)
		}
		info, err := os.Stat(localPath)
		if err != nil || !info.Mode().IsRegular() {
			// already remote-only?
			if size, _, ok, statErr := remote.Stat(ctx, it.kind, it.name); statErr == nil && ok {
				if it.size > 0 && size > 0 && size != it.size {
					log.Printf("size mismatch remote-only %s/%s local_db=%d remote=%d", it.kind, it.name, it.size, size)
					failed++
					continue
				}
				skipped++
				continue
			}
			log.Printf("missing local file %s", localPath)
			missing++
			continue
		}
		if size, _, ok, err := remote.Stat(ctx, it.kind, it.name); err == nil && ok && size == info.Size() {
			skipped++
			continue
		}
		if *dryRun {
			log.Printf("DRY-RUN upload %s/%s (%d bytes)", it.kind, it.name, info.Size())
			uploaded++
			continue
		}
		f, err := os.Open(localPath)
		if err != nil {
			log.Printf("open %s: %v", localPath, err)
			failed++
			continue
		}
		contentType := "application/octet-stream"
		if it.kind == creative.MediaKindVideo {
			contentType = "video/mp4"
		}
		putErr := remote.Put(ctx, it.kind, it.name, f, info.Size(), contentType)
		_ = f.Close()
		if putErr != nil {
			log.Printf("upload %s/%s: %v", it.kind, it.name, putErr)
			failed++
			continue
		}
		// verify
		size, _, ok, err := remote.Stat(ctx, it.kind, it.name)
		if err != nil || !ok || size != info.Size() {
			log.Printf("verify failed %s/%s remote=%d local=%d err=%v", it.kind, it.name, size, info.Size(), err)
			failed++
			continue
		}
		// optional hash-ish read check for small files
		if info.Size() <= 8<<20 {
			rc, err := remote.Open(ctx, it.kind, it.name, "")
			if err != nil {
				log.Printf("reopen %s/%s: %v", it.kind, it.name, err)
				failed++
				continue
			}
			n, _ := io.Copy(io.Discard, rc.Body)
			_ = rc.Body.Close()
			if n != info.Size() {
				log.Printf("read size mismatch %s/%s got=%d want=%d", it.kind, it.name, n, info.Size())
				failed++
				continue
			}
		}
		uploaded++
		log.Printf("uploaded %s/%s (%d bytes)", it.kind, it.name, info.Size())
	}

	log.Printf("done at %s uploaded=%d skipped=%d missing=%d failed=%d total=%d dry_run=%v",
		time.Now().Format(time.RFC3339), uploaded, skipped, missing, failed, len(items), *dryRun)
	if failed > 0 {
		os.Exit(2)
	}
}

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}
