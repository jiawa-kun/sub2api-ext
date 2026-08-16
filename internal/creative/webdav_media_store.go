package creative

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

// WebDAVMediaSettings configures an AList/other WebDAV endpoint.
type WebDAVMediaSettings struct {
	BaseURL    string
	Username   string
	Password   string
	Root       string
	HTTPClient *http.Client
}

// WebDAVMediaStore stores creative media through WebDAV.
type WebDAVMediaStore struct {
	baseURL  *url.URL
	username string
	password string
	root     string
	client   *http.Client
}

func NewWebDAVMediaStore(settings WebDAVMediaSettings) (*WebDAVMediaStore, error) {
	raw := strings.TrimSpace(settings.BaseURL)
	if raw == "" {
		return nil, fmt.Errorf("WebDAV URL 未配置")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("WebDAV URL 无效")
	}
	// Keep a directory-style base path.
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	client := settings.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	return &WebDAVMediaStore{
		baseURL:  u,
		username: settings.Username,
		password: settings.Password,
		root:     strings.Trim(strings.ReplaceAll(settings.Root, "\\", "/"), "/"),
		client:   client,
	}, nil
}

func (s *WebDAVMediaStore) Driver() string { return MediaDriverWebDAV }

func (s *WebDAVMediaStore) Ready() bool {
	return s != nil && s.baseURL != nil
}

func (s *WebDAVMediaStore) objectURL(kind, name string) (string, error) {
	if !s.Ready() {
		return "", fmt.Errorf("WebDAV 未配置")
	}
	folder, err := mediaFolder(kind)
	if err != nil {
		return "", err
	}
	name, err = validateMediaName(name)
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, 4)
	if s.root != "" {
		parts = append(parts, strings.Split(s.root, "/")...)
	}
	parts = append(parts, folder, name)
	rel := path.Join(parts...)
	u := *s.baseURL
	u.Path = path.Join(strings.TrimRight(s.baseURL.Path, "/"), rel)
	// path.Join cleans trailing slash away; files should not end with slash.
	return u.String(), nil
}

func (s *WebDAVMediaStore) dirURL(parts ...string) string {
	clean := make([]string, 0, len(parts)+2)
	if s.root != "" {
		clean = append(clean, strings.Split(s.root, "/")...)
	}
	for _, p := range parts {
		p = strings.Trim(p, "/")
		if p != "" {
			clean = append(clean, strings.Split(p, "/")...)
		}
	}
	u := *s.baseURL
	u.Path = path.Join(strings.TrimRight(s.baseURL.Path, "/"), path.Join(clean...)) + "/"
	return u.String()
}

func (s *WebDAVMediaStore) do(ctx context.Context, method, rawURL string, body io.Reader, contentLength int64, contentType, rangeHeader string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	if s.username != "" || s.password != "" {
		req.SetBasicAuth(s.username, s.password)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if contentLength >= 0 && body != nil {
		req.ContentLength = contentLength
	}
	if rangeHeader = strings.TrimSpace(rangeHeader); rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	return s.client.Do(req)
}

func (s *WebDAVMediaStore) ensureDirs(ctx context.Context, kind string) error {
	folder, err := mediaFolder(kind)
	if err != nil {
		return err
	}
	// Create root segments then images/videos folder.
	segments := []string{}
	if s.root != "" {
		for _, part := range strings.Split(s.root, "/") {
			if part == "" {
				continue
			}
			segments = append(segments, part)
			if err := s.mkcol(ctx, s.dirURL(strings.Join(segments, "/"))); err != nil {
				return err
			}
		}
	}
	return s.mkcol(ctx, s.dirURL(folder))
}

func (s *WebDAVMediaStore) mkcol(ctx context.Context, dirURL string) error {
	resp, err := s.do(ctx, "MKCOL", dirURL, nil, -1, "", "")
	if err != nil {
		return fmt.Errorf("WebDAV MKCOL: %w", err)
	}
	defer resp.Body.Close()
	// 201 created, 405/409 already exists depending on server.
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusOK, http.StatusMethodNotAllowed, http.StatusConflict:
		return nil
	default:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("WebDAV MKCOL HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
}

func (s *WebDAVMediaStore) Put(ctx context.Context, kind, name string, r io.Reader, size int64, contentType string) error {
	if err := s.ensureDirs(ctx, kind); err != nil {
		return err
	}
	rawURL, err := s.objectURL(kind, name)
	if err != nil {
		return err
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	resp, err := s.do(ctx, http.MethodPut, rawURL, r, size, contentType, "")
	if err != nil {
		return fmt.Errorf("WebDAV PUT: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("WebDAV PUT HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

func (s *WebDAVMediaStore) Open(ctx context.Context, kind, name, rangeHeader string) (*MediaContent, error) {
	rawURL, err := s.objectURL(kind, name)
	if err != nil {
		return nil, err
	}
	resp, err := s.do(ctx, http.MethodGet, rawURL, nil, -1, "", rangeHeader)
	if err != nil {
		return nil, fmt.Errorf("WebDAV GET: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return nil, errNotFound("远程媒体不存在")
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("WebDAV GET HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	modTime := time.Now().UTC()
	if raw := resp.Header.Get("Last-Modified"); raw != "" {
		if t, parseErr := http.ParseTime(raw); parseErr == nil {
			modTime = t
		}
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return &MediaContent{
		Body:          resp.Body,
		Name:          name,
		ModTime:       modTime,
		ContentType:   contentType,
		ContentLength: resp.ContentLength,
		ContentRange:  resp.Header.Get("Content-Range"),
		AcceptRanges:  defaultString(resp.Header.Get("Accept-Ranges"), "bytes"),
		StatusCode:    resp.StatusCode,
	}, nil
}

func (s *WebDAVMediaStore) Stat(ctx context.Context, kind, name string) (int64, time.Time, bool, error) {
	rawURL, err := s.objectURL(kind, name)
	if err != nil {
		return 0, time.Time{}, false, err
	}
	resp, err := s.do(ctx, http.MethodHead, rawURL, nil, -1, "", "")
	if err != nil {
		return 0, time.Time{}, false, fmt.Errorf("WebDAV HEAD: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return 0, time.Time{}, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		// Some WebDAV servers dislike HEAD; try a 0-byte range GET.
		getResp, getErr := s.do(ctx, http.MethodGet, rawURL, nil, -1, "", "bytes=0-0")
		if getErr != nil {
			return 0, time.Time{}, false, fmt.Errorf("WebDAV STAT: %w", getErr)
		}
		defer getResp.Body.Close()
		if getResp.StatusCode == http.StatusNotFound {
			return 0, time.Time{}, false, nil
		}
		if getResp.StatusCode != http.StatusOK && getResp.StatusCode != http.StatusPartialContent {
			b, _ := io.ReadAll(io.LimitReader(getResp.Body, 256))
			return 0, time.Time{}, false, fmt.Errorf("WebDAV STAT HTTP %d: %s", getResp.StatusCode, strings.TrimSpace(string(b)))
		}
		size := getResp.ContentLength
		if cr := getResp.Header.Get("Content-Range"); cr != "" {
			if parts := strings.Split(cr, "/"); len(parts) == 2 {
				if n, parseErr := strconv.ParseInt(parts[1], 10, 64); parseErr == nil {
					size = n
				}
			}
		}
		modTime := time.Now().UTC()
		if raw := getResp.Header.Get("Last-Modified"); raw != "" {
			if t, parseErr := http.ParseTime(raw); parseErr == nil {
				modTime = t
			}
		}
		return size, modTime, true, nil
	}
	modTime := time.Now().UTC()
	if raw := resp.Header.Get("Last-Modified"); raw != "" {
		if t, parseErr := http.ParseTime(raw); parseErr == nil {
			modTime = t
		}
	}
	return resp.ContentLength, modTime, true, nil
}

func (s *WebDAVMediaStore) Delete(ctx context.Context, kind, name string) error {
	rawURL, err := s.objectURL(kind, name)
	if err != nil {
		return err
	}
	resp, err := s.do(ctx, http.MethodDelete, rawURL, nil, -1, "", "")
	if err != nil {
		return fmt.Errorf("WebDAV DELETE: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errNotFound("远程媒体不存在")
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("WebDAV DELETE HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}
