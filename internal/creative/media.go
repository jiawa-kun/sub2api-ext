package creative

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"sub2api-ext/internal/store"
)

const (
	maxArchivedImageBytes int64 = 64 << 20
	maxArchivedVideoBytes int64 = 2 << 30
)

type upstreamImage struct {
	URL     string `json:"url"`
	B64JSON string `json:"b64_json"`
}

func (s *Service) mediaReady() bool {
	return s != nil && s.mediaStore != nil && s.mediaStore.Ready()
}

func (s *Service) archiveImages(ctx context.Context, job *store.CreativeJob, body []byte) ([]store.CreativeJobImage, string, error) {
	if job == nil || job.MediaType != "image" || job.ID <= 0 {
		return nil, "", fmt.Errorf("图片任务无效")
	}
	if !s.mediaReady() {
		return nil, "", fmt.Errorf("媒体存储未配置")
	}
	var payload struct {
		Data []upstreamImage `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || len(payload.Data) == 0 {
		return nil, "", fmt.Errorf("图片接口未返回图片")
	}
	remoteJob := *job
	remoteJob.ResultJSON = string(body)
	images := make([]store.CreativeJobImage, 0, len(payload.Data))
	cleanup := func() {
		s.removeArchivedImages(images)
	}
	for index, item := range payload.Data {
		var content io.ReadCloser
		if encoded := strings.TrimSpace(item.B64JSON); encoded != "" {
			reader, err := imageBase64Reader(encoded)
			if err != nil {
				cleanup()
				return nil, "", fmt.Errorf("解码第 %d 张图片: %w", index+1, err)
			}
			content = io.NopCloser(reader)
		} else if rawURL := strings.TrimSpace(item.URL); strings.HasPrefix(strings.ToLower(rawURL), "data:") {
			reader, err := imageBase64Reader(rawURL)
			if err != nil {
				cleanup()
				return nil, "", fmt.Errorf("解码第 %d 张图片: %w", index+1, err)
			}
			content = io.NopCloser(reader)
		} else if rawURL != "" {
			remote, err := s.openRemoteJobContent(ctx, &remoteJob, index, "")
			if err != nil {
				cleanup()
				return nil, "", fmt.Errorf("下载第 %d 张图片: %w", index+1, err)
			}
			content = remote.Body
		} else {
			cleanup()
			return nil, "", fmt.Errorf("第 %d 张图片没有可保存的内容", index+1)
		}
		image, err := s.archiveImageReader(ctx, job.ID, index, content)
		_ = content.Close()
		if err != nil {
			cleanup()
			return nil, "", fmt.Errorf("保存第 %d 张图片: %w", index+1, err)
		}
		images = append(images, image)
	}
	sanitized, err := sanitizeImageResult(body)
	if err != nil {
		cleanup()
		return nil, "", err
	}
	return images, sanitized, nil
}

func (s *Service) archiveExistingImages(ctx context.Context, job *store.CreativeJob) error {
	if job == nil || job.MediaType != "image" || !s.mediaReady() {
		return fmt.Errorf("媒体存储未配置")
	}
	s.mediaMu.Lock()
	defer s.mediaMu.Unlock()
	existing, err := s.store.ListCreativeJobImages(ctx, job.ID)
	if err != nil {
		return err
	}
	if s.storedImagesAvailable(ctx, job, existing) {
		return nil
	}
	images, sanitizedResult, err := s.archiveImages(ctx, job, []byte(job.ResultJSON))
	if err != nil {
		return err
	}
	updated := *job
	updated.ResultJSON = sanitizedResult
	if err := s.store.CompleteCreativeImageJob(ctx, updated, images); err != nil {
		s.removeArchivedImages(images)
		return err
	}
	job.ResultJSON = sanitizedResult
	s.removeArchivedImages(existing)
	return nil
}

func (s *Service) storedImagesAvailable(ctx context.Context, job *store.CreativeJob, images []store.CreativeJobImage) bool {
	var result struct {
		Data []json.RawMessage `json:"data"`
	}
	if json.Unmarshal([]byte(job.ResultJSON), &result) != nil || len(result.Data) == 0 || len(images) != len(result.Data) {
		return false
	}
	for index, image := range images {
		if image.ImageIndex != index {
			return false
		}
		size, _, ok, err := s.mediaStore.Stat(ctx, MediaKindImage, image.LocalMediaFile)
		if err != nil || !ok || size != image.LocalMediaSize {
			return false
		}
	}
	return true
}

func (s *Service) removeArchivedImages(images []store.CreativeJobImage) {
	if !s.mediaReady() {
		return
	}
	for _, image := range images {
		_ = s.mediaStore.Delete(context.Background(), MediaKindImage, image.LocalMediaFile)
	}
}

func imageBase64Reader(encoded string) (io.Reader, error) {
	if strings.HasPrefix(strings.ToLower(encoded), "data:") {
		parts := strings.SplitN(encoded, ",", 2)
		if len(parts) != 2 || !strings.Contains(strings.ToLower(parts[0]), ";base64") {
			return nil, fmt.Errorf("Base64 图片格式无效")
		}
		encoded = parts[1]
	}
	return base64.NewDecoder(base64.StdEncoding, strings.NewReader(encoded)), nil
}

func sanitizeImageResult(body []byte) (string, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("解析图片结果: %w", err)
	}
	if items, ok := payload["data"].([]any); ok {
		for _, item := range items {
			if image, ok := item.(map[string]any); ok {
				delete(image, "b64_json")
				if rawURL, ok := image["url"].(string); ok && strings.HasPrefix(strings.ToLower(strings.TrimSpace(rawURL)), "data:") {
					delete(image, "url")
				}
				image["local"] = true
			}
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("保存图片结果: %w", err)
	}
	return string(raw), nil
}

func (s *Service) archiveImageReader(ctx context.Context, jobID int64, index int, source io.Reader) (store.CreativeJobImage, error) {
	if !s.mediaReady() {
		return store.CreativeJobImage{}, fmt.Errorf("媒体存储未配置")
	}
	limited := io.LimitReader(source, maxArchivedImageBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return store.CreativeJobImage{}, err
	}
	if len(data) == 0 {
		return store.CreativeJobImage{}, fmt.Errorf("图片内容为空")
	}
	if int64(len(data)) > maxArchivedImageBytes {
		return store.CreativeJobImage{}, fmt.Errorf("图片超过 64 MiB 上限")
	}
	contentType, extension, err := archivedImageFormat(data)
	if err != nil {
		return store.CreativeJobImage{}, err
	}
	name := fmt.Sprintf("image-%d-%d-%s%s", jobID, index+1, newToken(4), extension)
	if err := s.mediaStore.Put(ctx, MediaKindImage, name, bytes.NewReader(data), int64(len(data)), contentType); err != nil {
		return store.CreativeJobImage{}, err
	}
	return store.CreativeJobImage{
		JobID:          jobID,
		ImageIndex:     index,
		LocalMediaFile: name,
		LocalMediaType: contentType,
		LocalMediaSize: int64(len(data)),
	}, nil
}

func archivedImageFormat(head []byte) (string, string, error) {
	contentType := http.DetectContentType(head)
	switch contentType {
	case "image/png":
		return contentType, ".png", nil
	case "image/jpeg":
		return contentType, ".jpg", nil
	case "image/webp":
		return contentType, ".webp", nil
	default:
		return "", "", fmt.Errorf("不支持的图片内容类型 %s", contentType)
	}
}

func (s *Service) archiveVideo(ctx context.Context, job *store.CreativeJob) error {
	if job == nil || job.MediaType != "video" || !s.mediaReady() || job.LocalMediaFile != "" {
		return nil
	}
	name := fmt.Sprintf("video-%d.mp4", job.ID)
	if size, _, ok, err := s.mediaStore.Stat(ctx, MediaKindVideo, name); err == nil && ok {
		job.LocalMediaFile = name
		job.LocalMediaType = "video/mp4"
		job.LocalMediaSize = size
		return nil
	}

	content, err := s.openRemoteJobContent(ctx, job, 0, "")
	if err != nil {
		return fmt.Errorf("归档视频: %w", err)
	}
	defer content.Body.Close()
	if content.ContentLength > maxArchivedVideoBytes {
		return fmt.Errorf("归档视频超过 2 GiB 上限")
	}

	tmp, err := os.CreateTemp("", "creative-video-*.tmp")
	if err != nil {
		return fmt.Errorf("创建视频临时文件: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()
	n, copyErr := io.Copy(tmp, io.LimitReader(content.Body, maxArchivedVideoBytes+1))
	if copyErr != nil {
		return fmt.Errorf("写入归档视频: %w", copyErr)
	}
	if n > maxArchivedVideoBytes {
		return fmt.Errorf("归档视频超过 2 GiB 上限")
	}
	if n == 0 {
		return fmt.Errorf("归档视频内容为空")
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("回读归档视频: %w", err)
	}
	contentType := normalizeVideoContentType(content.ContentType)
	if err := s.mediaStore.Put(ctx, MediaKindVideo, name, tmp, n, contentType); err != nil {
		return err
	}
	job.LocalMediaFile = name
	job.LocalMediaType = contentType
	job.LocalMediaSize = n
	return nil
}

func (s *Service) openStoredMedia(ctx context.Context, job *store.CreativeJob, rangeHeader string) (*MediaContent, error) {
	if job == nil || !s.mediaReady() || job.LocalMediaFile == "" {
		return nil, nil
	}
	content, err := s.mediaStore.Open(ctx, MediaKindVideo, job.LocalMediaFile, rangeHeader)
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	content.ContentType = normalizeVideoContentType(firstNonEmpty(content.ContentType, job.LocalMediaType))
	if content.Name == "" {
		content.Name = job.LocalMediaFile
	}
	if content.StatusCode == 0 {
		content.StatusCode = http.StatusOK
	}
	return content, nil
}

func (s *Service) openStoredImage(ctx context.Context, job *store.CreativeJob, index int, rangeHeader string) (*MediaContent, error) {
	if job == nil || job.MediaType != "image" || !s.mediaReady() {
		return nil, nil
	}
	image, err := s.store.GetCreativeJobImage(ctx, job.ID, index)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	content, err := s.mediaStore.Open(ctx, MediaKindImage, image.LocalMediaFile, rangeHeader)
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	if content.ContentLength >= 0 && image.LocalMediaSize > 0 && content.ContentLength != image.LocalMediaSize && content.StatusCode == http.StatusOK {
		_ = content.Body.Close()
		return nil, nil
	}
	if ct := strings.TrimSpace(image.LocalMediaType); ct != "" {
		content.ContentType = ct
	}
	if content.Name == "" {
		content.Name = image.LocalMediaFile
	}
	if content.StatusCode == 0 {
		content.StatusCode = http.StatusOK
	}
	return content, nil
}

func (s *Service) removeLocalMedia(ctx context.Context, job *store.CreativeJob) {
	if !s.mediaReady() || job == nil {
		return
	}
	if job.MediaType == "image" {
		s.mediaMu.Lock()
		defer s.mediaMu.Unlock()
		images, err := s.store.ListCreativeJobImages(ctx, job.ID)
		if err == nil {
			allRemoved := true
			for _, image := range images {
				if removeErr := s.mediaStore.Delete(ctx, MediaKindImage, image.LocalMediaFile); removeErr != nil && !isNotFound(removeErr) {
					allRemoved = false
				}
			}
			if allRemoved {
				_ = s.store.DeleteCreativeJobImages(ctx, job.ID)
			}
		}
	}
	if job.LocalMediaFile == "" {
		return
	}
	_ = s.mediaStore.Delete(ctx, MediaKindVideo, job.LocalMediaFile)
}

// localMediaPath remains for tests and recovery tools that still inspect local layout.
func (s *Service) localMediaPath(name string) (string, error) {
	if local, ok := s.mediaStore.(*LocalMediaStore); ok {
		return local.pathFor(MediaKindVideo, name)
	}
	if strings.TrimSpace(s.mediaRoot) == "" {
		return "", fmt.Errorf("本地媒体目录未配置")
	}
	name, err := validateMediaName(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.mediaRoot, name), nil
}

func normalizeVideoContentType(value string) string {
	if parsed, _, err := mime.ParseMediaType(strings.TrimSpace(value)); err == nil && strings.HasPrefix(parsed, "video/") {
		return parsed
	}
	return "video/mp4"
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
