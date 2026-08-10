package creative

import (
	"context"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"sub2api-ext/internal/store"
)

const maxArchivedVideoBytes int64 = 2 << 30

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

func (s *Service) removeLocalMedia(job *store.CreativeJob) {
	if job == nil || job.LocalMediaFile == "" {
		return
	}
	path, err := s.localMediaPath(job.LocalMediaFile)
	if err == nil {
		_ = os.Remove(path)
	}
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
