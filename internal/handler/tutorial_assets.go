package handler

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const tutorialAssetMaxBytes int64 = 5 << 20

var tutorialAssetNamePattern = regexp.MustCompile(`^[a-f0-9]{32}\.(?:png|jpg|webp|gif)$`)

var tutorialAssetTypes = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/webp": ".webp",
	"image/gif":  ".gif",
}

// AdminUploadTutorialAsset stores an image beside the SQLite database so the
// existing data volume keeps it across container replacements.
func (h *Handler) AdminUploadTutorialAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.limitAdminWrite.Allow("ATU:" + clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	if _, err := h.requireAdmin(r); err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, tutorialAssetMaxBytes+(1<<20))
	if err := r.ParseMultipartForm(tutorialAssetMaxBytes); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeErr(w, http.StatusRequestEntityTooLarge, "图片不能超过 5MB")
			return
		}
		writeErr(w, http.StatusBadRequest, "invalid multipart form: "+err.Error())
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "缺少图片文件")
		return
	}
	defer file.Close()
	if header.Size <= 0 {
		writeErr(w, http.StatusBadRequest, "图片内容为空")
		return
	}
	if header.Size > tutorialAssetMaxBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "图片不能超过 5MB")
		return
	}

	head := make([]byte, 512)
	n, readErr := io.ReadFull(file, head)
	if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		writeErr(w, http.StatusBadRequest, "读取图片失败")
		return
	}
	contentType := http.DetectContentType(head[:n])
	ext, ok := tutorialAssetTypes[contentType]
	if !ok {
		writeErr(w, http.StatusUnsupportedMediaType, "仅支持 PNG、JPG、WebP、GIF 图片")
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		writeErr(w, http.StatusBadRequest, "读取图片失败")
		return
	}

	assetDir := h.tutorialAssetDir()
	if err := os.MkdirAll(assetDir, 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, "创建图片目录失败")
		return
	}
	filename, err := randomTutorialAssetName(ext)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "生成图片文件名失败")
		return
	}
	tmp, err := os.CreateTemp(assetDir, ".tutorial-upload-*")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "保存图片失败")
		return
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()

	written, err := io.Copy(tmp, io.LimitReader(file, tutorialAssetMaxBytes+1))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "保存图片失败")
		return
	}
	if written > tutorialAssetMaxBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "图片不能超过 5MB")
		return
	}
	if err := tmp.Chmod(0o644); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存图片失败")
		return
	}
	if err := tmp.Close(); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存图片失败")
		return
	}
	if err := os.Rename(tmpName, filepath.Join(assetDir, filename)); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存图片失败")
		return
	}
	committed = true

	writeJSON(w, http.StatusCreated, map[string]any{
		"filename": filename,
		"url":      "./tutorial-assets/" + filename,
		"size":     written,
		"type":     contentType,
	})
}

// PublicTutorialAsset serves generated image names only and never exposes a
// directory listing or arbitrary files from the data volume.
func (h *Handler) PublicTutorialAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	const marker = "/tutorial-assets/"
	idx := strings.LastIndex(r.URL.Path, marker)
	if idx < 0 {
		http.NotFound(w, r)
		return
	}
	name := r.URL.Path[idx+len(marker):]
	if !tutorialAssetNamePattern.MatchString(name) {
		http.NotFound(w, r)
		return
	}

	file, err := os.Open(filepath.Join(h.tutorialAssetDir(), name))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", tutorialAssetContentType(filepath.Ext(name)))
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, name, info.ModTime(), file)
}

func (h *Handler) tutorialAssetDir() string {
	dbPath := strings.TrimSpace(h.cfg.Store.SQLitePath)
	if dbPath == "" {
		dbPath = filepath.Join("data", "checkin.db")
	}
	return filepath.Join(filepath.Dir(dbPath), "tutorial-assets")
}

func randomTutorialAssetName(ext string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b) + ext, nil
}

func tutorialAssetContentType(ext string) string {
	switch strings.ToLower(ext) {
	case ".png":
		return "image/png"
	case ".jpg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	default:
		return "application/octet-stream"
	}
}
