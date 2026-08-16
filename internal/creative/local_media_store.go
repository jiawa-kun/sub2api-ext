package creative

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LocalMediaStore keeps creative media on the local filesystem.
// LocalVideoRoot points at .../creative/videos; images live in the sibling images directory.
type LocalMediaStore struct {
	videoRoot string
	imageRoot string
}

func NewLocalMediaStore(videoRoot string) *LocalMediaStore {
	videoRoot = strings.TrimSpace(videoRoot)
	if videoRoot == "" {
		return &LocalMediaStore{}
	}
	return &LocalMediaStore{
		videoRoot: videoRoot,
		imageRoot: filepath.Join(filepath.Dir(videoRoot), "images"),
	}
}

func (s *LocalMediaStore) Driver() string { return MediaDriverLocal }

func (s *LocalMediaStore) Ready() bool {
	return s != nil && strings.TrimSpace(s.videoRoot) != ""
}

func (s *LocalMediaStore) rootFor(kind string) (string, error) {
	if s == nil || !s.Ready() {
		return "", fmt.Errorf("本地媒体目录未配置")
	}
	switch strings.TrimSpace(kind) {
	case MediaKindImage:
		return s.imageRoot, nil
	case MediaKindVideo:
		return s.videoRoot, nil
	default:
		return "", fmt.Errorf("未知媒体类型 %q", kind)
	}
}

func (s *LocalMediaStore) pathFor(kind, name string) (string, error) {
	root, err := s.rootFor(kind)
	if err != nil {
		return "", err
	}
	name, err = validateMediaName(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, name), nil
}

func (s *LocalMediaStore) Put(ctx context.Context, kind, name string, r io.Reader, size int64, contentType string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := s.rootFor(kind)
	if err != nil {
		return err
	}
	name, err = validateMediaName(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o750); err != nil {
		return fmt.Errorf("创建媒体目录: %w", err)
	}
	path := filepath.Join(root, name)
	tmp, err := os.CreateTemp(root, ".media-*.tmp")
	if err != nil {
		return fmt.Errorf("创建媒体临时文件: %w", err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()
	written, err := io.Copy(tmp, r)
	if err != nil {
		return fmt.Errorf("写入媒体: %w", err)
	}
	if size > 0 && written != size {
		return fmt.Errorf("媒体大小不匹配: got %d want %d", written, size)
	}
	if written == 0 {
		return fmt.Errorf("媒体内容为空")
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("同步媒体: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭媒体临时文件: %w", err)
	}
	if err := os.Chmod(tmpName, 0o640); err != nil {
		return fmt.Errorf("设置媒体权限: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("提交媒体: %w", err)
	}
	committed = true
	_ = contentType
	return nil
}

func (s *LocalMediaStore) Open(ctx context.Context, kind, name, rangeHeader string) (*MediaContent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := s.pathFor(kind, name)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, errNotFound("本地媒体不存在")
	}
	if err != nil {
		return nil, fmt.Errorf("打开本地媒体: %w", err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("本地媒体无效")
	}
	_ = rangeHeader
	return &MediaContent{
		Body:          file,
		ReadSeeker:    file,
		Name:          name,
		ModTime:       info.ModTime(),
		ContentType:   "application/octet-stream",
		ContentLength: info.Size(),
		AcceptRanges:  "bytes",
		StatusCode:    http.StatusOK,
	}, nil
}

func (s *LocalMediaStore) Stat(ctx context.Context, kind, name string) (int64, time.Time, bool, error) {
	if err := ctx.Err(); err != nil {
		return 0, time.Time{}, false, err
	}
	path, err := s.pathFor(kind, name)
	if err != nil {
		return 0, time.Time{}, false, err
	}
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return 0, time.Time{}, false, nil
	}
	if err != nil {
		return 0, time.Time{}, false, err
	}
	if !info.Mode().IsRegular() {
		return 0, time.Time{}, false, nil
	}
	return info.Size(), info.ModTime(), true, nil
}

func (s *LocalMediaStore) Delete(ctx context.Context, kind, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := s.pathFor(kind, name)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return errNotFound("本地媒体不存在")
		}
		return err
	}
	return nil
}
