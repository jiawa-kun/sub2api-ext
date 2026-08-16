package creative

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"
)

const (
	MediaKindImage = "image"
	MediaKindVideo = "video"

	MediaDriverLocal  = "local"
	MediaDriverWebDAV = "webdav"
)

// MediaStore persists archived creative media binaries.
type MediaStore interface {
	Driver() string
	Ready() bool
	Put(ctx context.Context, kind, name string, r io.Reader, size int64, contentType string) error
	Open(ctx context.Context, kind, name, rangeHeader string) (*MediaContent, error)
	Stat(ctx context.Context, kind, name string) (size int64, modTime time.Time, ok bool, err error)
	Delete(ctx context.Context, kind, name string) error
}

// MediaSettings configures archived creative media storage.
type MediaSettings struct {
	Driver         string
	LocalVideoRoot string
	WebDAVURL      string
	WebDAVUsername string
	WebDAVPassword string
	WebDAVRoot     string
	// LocalFallback keeps reading/deleting old local files after switching to WebDAV.
	// nil means default true for webdav, false for local.
	LocalFallback *bool
	HTTPClient    *http.Client
}

func validateMediaName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || path.Base(name) != name || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("媒体文件名无效")
	}
	return name, nil
}

func mediaFolder(kind string) (string, error) {
	switch strings.TrimSpace(kind) {
	case MediaKindImage:
		return "images", nil
	case MediaKindVideo:
		return "videos", nil
	default:
		return "", fmt.Errorf("未知媒体类型 %q", kind)
	}
}

// NewMediaStore builds the configured media backend.
func NewMediaStore(settings MediaSettings) (MediaStore, error) {
	driver := strings.ToLower(strings.TrimSpace(settings.Driver))
	if driver == "" {
		driver = MediaDriverLocal
	}
	localRoot := strings.TrimSpace(settings.LocalVideoRoot)
	var local MediaStore
	if localRoot != "" {
		local = NewLocalMediaStore(localRoot)
	}

	switch driver {
	case MediaDriverLocal:
		if local == nil {
			return nil, fmt.Errorf("本地媒体目录未配置")
		}
		return local, nil
	case MediaDriverWebDAV:
		webdav, err := NewWebDAVMediaStore(WebDAVMediaSettings{
			BaseURL:    settings.WebDAVURL,
			Username:   settings.WebDAVUsername,
			Password:   settings.WebDAVPassword,
			Root:       settings.WebDAVRoot,
			HTTPClient: settings.HTTPClient,
		})
		if err != nil {
			return nil, err
		}
		fallback := true
		if settings.LocalFallback != nil {
			fallback = *settings.LocalFallback
		}
		if fallback && local != nil {
			return &fallbackMediaStore{primary: webdav, fallback: local}, nil
		}
		return webdav, nil
	default:
		return nil, fmt.Errorf("不支持的媒体存储驱动 %q", settings.Driver)
	}
}

type fallbackMediaStore struct {
	primary  MediaStore
	fallback MediaStore
}

func (s *fallbackMediaStore) Driver() string {
	if s == nil || s.primary == nil {
		return ""
	}
	return s.primary.Driver()
}

func (s *fallbackMediaStore) Ready() bool {
	return s != nil && s.primary != nil && s.primary.Ready()
}

func (s *fallbackMediaStore) Put(ctx context.Context, kind, name string, r io.Reader, size int64, contentType string) error {
	return s.primary.Put(ctx, kind, name, r, size, contentType)
}

func (s *fallbackMediaStore) Open(ctx context.Context, kind, name, rangeHeader string) (*MediaContent, error) {
	content, err := s.primary.Open(ctx, kind, name, rangeHeader)
	if content != nil || (err != nil && !isNotFound(err)) {
		return content, err
	}
	if s.fallback == nil {
		return nil, err
	}
	return s.fallback.Open(ctx, kind, name, rangeHeader)
}

func (s *fallbackMediaStore) Stat(ctx context.Context, kind, name string) (int64, time.Time, bool, error) {
	size, modTime, ok, err := s.primary.Stat(ctx, kind, name)
	if ok || (err != nil && !isNotFound(err)) {
		return size, modTime, ok, err
	}
	if s.fallback == nil {
		return size, modTime, ok, err
	}
	return s.fallback.Stat(ctx, kind, name)
}

func (s *fallbackMediaStore) Delete(ctx context.Context, kind, name string) error {
	var first error
	if s.primary != nil {
		if err := s.primary.Delete(ctx, kind, name); err != nil && !isNotFound(err) {
			first = err
		}
	}
	if s.fallback != nil {
		if err := s.fallback.Delete(ctx, kind, name); err != nil && !isNotFound(err) && first == nil {
			first = err
		}
	}
	return first
}

type notFoundError struct {
	msg string
}

func (e *notFoundError) Error() string {
	if e == nil || e.msg == "" {
		return "media not found"
	}
	return e.msg
}

func errNotFound(msg string) error {
	return &notFoundError{msg: msg}
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var nf *notFoundError
	return errors.As(err, &nf)
}
