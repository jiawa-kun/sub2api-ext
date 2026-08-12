package creative

import (
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

func (s *Service) archiveImages(ctx context.Context, job *store.CreativeJob, body []byte) ([]store.CreativeJobImage, string, error) {
	if job == nil || job.MediaType != "image" || job.ID <= 0 {
		return nil, "", fmt.Errorf("图片任务无效")
	}
	if s.imageMediaRoot() == "" {
		return nil, "", fmt.Errorf("本地图片目录未配置")
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
		for _, image := range images {
			if path, err := s.localImagePath(image.LocalMediaFile); err == nil {
				_ = os.Remove(path)
			}
		}
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
		image, err := s.archiveImageReader(job.ID, index, content)
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
	if job == nil || job.MediaType != "image" || s.imageMediaRoot() == "" {
		return fmt.Errorf("本地图片目录未配置")
	}
	s.mediaMu.Lock()
	defer s.mediaMu.Unlock()
	existing, err := s.store.ListCreativeJobImages(ctx, job.ID)
	if err != nil {
		return err
	}
	if s.localImagesAvailable(job, existing) {
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

func (s *Service) localImagesAvailable(job *store.CreativeJob, images []store.CreativeJobImage) bool {
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
		path, err := s.localImagePath(image.LocalMediaFile)
		if err != nil {
			return false
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() != image.LocalMediaSize {
			return false
		}
	}
	return true
}

func (s *Service) removeArchivedImages(images []store.CreativeJobImage) {
	for _, image := range images {
		if path, err := s.localImagePath(image.LocalMediaFile); err == nil {
			_ = os.Remove(path)
		}
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

func (s *Service) archiveImageReader(jobID int64, index int, source io.Reader) (store.CreativeJobImage, error) {
	root := s.imageMediaRoot()
	if err := os.MkdirAll(root, 0o750); err != nil {
		return store.CreativeJobImage{}, fmt.Errorf("创建图片目录: %w", err)
	}
	head := make([]byte, 512)
	n, readErr := io.ReadFull(source, head)
	if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
		return store.CreativeJobImage{}, readErr
	}
	head = head[:n]
	if len(head) == 0 {
		return store.CreativeJobImage{}, fmt.Errorf("图片内容为空")
	}
	contentType, extension, err := archivedImageFormat(head)
	if err != nil {
		return store.CreativeJobImage{}, err
	}
	name := fmt.Sprintf("image-%d-%d-%s%s", jobID, index+1, newToken(4), extension)
	path, err := s.localImagePath(name)
	if err != nil {
		return store.CreativeJobImage{}, err
	}
	tmp, err := os.CreateTemp(root, ".image-*.tmp")
	if err != nil {
		return store.CreativeJobImage{}, fmt.Errorf("创建图片临时文件: %w", err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(head); err != nil {
		return store.CreativeJobImage{}, err
	}
	remaining := maxArchivedImageBytes - int64(len(head))
	written, err := io.Copy(tmp, io.LimitReader(source, remaining+1))
	if err != nil {
		return store.CreativeJobImage{}, err
	}
	size := int64(len(head)) + written
	if size > maxArchivedImageBytes {
		return store.CreativeJobImage{}, fmt.Errorf("图片超过 64 MiB 上限")
	}
	if err := tmp.Sync(); err != nil {
		return store.CreativeJobImage{}, err
	}
	if err := tmp.Close(); err != nil {
		return store.CreativeJobImage{}, err
	}
	if err := os.Chmod(tmpName, 0o640); err != nil {
		return store.CreativeJobImage{}, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return store.CreativeJobImage{}, err
	}
	committed = true
	return store.CreativeJobImage{JobID: jobID, ImageIndex: index, LocalMediaFile: name, LocalMediaType: contentType, LocalMediaSize: size}, nil
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
	if job == nil || job.MediaType != "video" || s.mediaRoot == "" || job.LocalMediaFile != "" {
		return nil
	}
	name := fmt.Sprintf("video-%d.mp4", job.ID)
	path, err := s.localMediaPath(name)
	if err != nil {
		return err
	}
	if info, statErr := os.Stat(path); statErr == nil && info.Mode().IsRegular() {
		job.LocalMediaFile = name
		job.LocalMediaType = "video/mp4"
		job.LocalMediaSize = info.Size()
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
	if err := os.MkdirAll(s.mediaRoot, 0o750); err != nil {
		return fmt.Errorf("创建视频目录: %w", err)
	}
	tmp, err := os.CreateTemp(s.mediaRoot, ".video-*.tmp")
	if err != nil {
		return fmt.Errorf("创建视频临时文件: %w", err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpName)
		}
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
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("同步归档视频: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭归档视频: %w", err)
	}
	if err := os.Chmod(tmpName, 0o640); err != nil {
		return fmt.Errorf("设置归档视频权限: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("提交归档视频: %w", err)
	}
	committed = true
	job.LocalMediaFile = name
	job.LocalMediaType = normalizeVideoContentType(content.ContentType)
	job.LocalMediaSize = n
	return nil
}

func (s *Service) openLocalMedia(job *store.CreativeJob) (*MediaContent, error) {
	if job == nil || s.mediaRoot == "" || job.LocalMediaFile == "" {
		return nil, nil
	}
	path, err := s.localMediaPath(job.LocalMediaFile)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("打开本地视频: %w", err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("本地视频无效")
	}
	return &MediaContent{
		Body: file, ReadSeeker: file, Name: job.LocalMediaFile, ModTime: info.ModTime(),
		ContentType: normalizeVideoContentType(job.LocalMediaType), ContentLength: info.Size(),
		AcceptRanges: "bytes", StatusCode: 200,
	}, nil
}

func (s *Service) openLocalImage(ctx context.Context, job *store.CreativeJob, index int) (*MediaContent, error) {
	if job == nil || job.MediaType != "image" || s.imageMediaRoot() == "" {
		return nil, nil
	}
	image, err := s.store.GetCreativeJobImage(ctx, job.ID, index)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	path, err := s.localImagePath(image.LocalMediaFile)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("打开本地图片: %w", err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("本地图片无效")
	}
	if info.Size() != image.LocalMediaSize {
		_ = file.Close()
		return nil, nil
	}
	return &MediaContent{
		Body: file, ReadSeeker: file, Name: image.LocalMediaFile, ModTime: info.ModTime(),
		ContentType: image.LocalMediaType, ContentLength: info.Size(), AcceptRanges: "bytes", StatusCode: http.StatusOK,
	}, nil
}

func (s *Service) removeLocalMedia(ctx context.Context, job *store.CreativeJob) {
	if job != nil && job.MediaType == "image" {
		s.mediaMu.Lock()
		defer s.mediaMu.Unlock()
		images, err := s.store.ListCreativeJobImages(ctx, job.ID)
		if err == nil {
			allRemoved := true
			for _, image := range images {
				path, pathErr := s.localImagePath(image.LocalMediaFile)
				if pathErr != nil {
					allRemoved = false
					continue
				}
				if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
					allRemoved = false
				}
			}
			if allRemoved {
				_ = s.store.DeleteCreativeJobImages(ctx, job.ID)
			}
		}
	}
	if job == nil || job.LocalMediaFile == "" {
		return
	}
	path, err := s.localMediaPath(job.LocalMediaFile)
	if err == nil {
		_ = os.Remove(path)
	}
}

func (s *Service) imageMediaRoot() string {
	if strings.TrimSpace(s.mediaRoot) == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(s.mediaRoot), "images")
}

func (s *Service) localImagePath(name string) (string, error) {
	root := s.imageMediaRoot()
	if root == "" {
		return "", fmt.Errorf("本地图片目录未配置")
	}
	name = strings.TrimSpace(name)
	if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, `/\\`) {
		return "", fmt.Errorf("本地图片文件名无效")
	}
	return filepath.Join(root, name), nil
}

func (s *Service) localMediaPath(name string) (string, error) {
	if strings.TrimSpace(s.mediaRoot) == "" {
		return "", fmt.Errorf("本地媒体目录未配置")
	}
	name = strings.TrimSpace(name)
	if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, `/\\`) {
		return "", fmt.Errorf("本地媒体文件名无效")
	}
	return filepath.Join(s.mediaRoot, name), nil
}

func normalizeVideoContentType(value string) string {
	if parsed, _, err := mime.ParseMediaType(strings.TrimSpace(value)); err == nil && strings.HasPrefix(parsed, "video/") {
		return parsed
	}
	return "video/mp4"
}
