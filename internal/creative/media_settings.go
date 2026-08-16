package creative

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"sub2api-ext/internal/store"
)

const (
	KeyMediaDriver        = "creative_media_driver"
	KeyWebDAVURL          = "creative_webdav_url"
	KeyWebDAVUsername     = "creative_webdav_username"
	KeyWebDAVPassword     = "creative_webdav_password"
	KeyWebDAVRoot         = "creative_webdav_root"
	KeyMediaLocalFallback = "creative_media_local_fallback"
)

// MediaRuntime is the hot-reloadable creative media storage configuration.
type MediaRuntime struct {
	Driver         string `json:"driver"`
	LocalVideoRoot string `json:"local_root"`
	WebDAVURL      string `json:"webdav_url"`
	WebDAVUsername string `json:"webdav_username"`
	WebDAVPassword string `json:"-"`
	WebDAVRoot     string `json:"webdav_root"`
	LocalFallback  bool   `json:"local_fallback"`
}

// MediaUpdateInput allows partial admin updates.
type MediaUpdateInput struct {
	Driver         *string `json:"driver"`
	WebDAVURL      *string `json:"webdav_url"`
	WebDAVUsername *string `json:"webdav_username"`
	WebDAVPassword *string `json:"webdav_password"`
	PasswordClear  *bool   `json:"password_clear"`
	WebDAVRoot     *string `json:"webdav_root"`
	LocalFallback  *bool   `json:"local_fallback"`
}

// MediaConfig holds runtime media storage config backed by app_settings.
type MediaConfig struct {
	mu       sync.RWMutex
	store    *store.Store
	defaults MediaRuntime
	current  MediaRuntime
}

func NewMediaConfig(st *store.Store, defaults MediaRuntime) *MediaConfig {
	defaults = normalizeMediaRuntime(defaults)
	c := &MediaConfig{store: st, defaults: defaults, current: defaults}
	_ = c.Reload(context.Background())
	return c
}

func (c *MediaConfig) Get() MediaRuntime {
	if c == nil {
		return MediaRuntime{Driver: MediaDriverLocal, LocalFallback: true}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.current
}

func (c *MediaConfig) Defaults() MediaRuntime {
	if c == nil {
		return MediaRuntime{Driver: MediaDriverLocal, LocalFallback: true}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.defaults
}

func (c *MediaConfig) Reload(ctx context.Context) error {
	if c == nil || c.store == nil {
		return nil
	}
	rt := c.defaults
	get := func(key string) (string, bool) {
		v, ok, err := c.store.GetSetting(ctx, key)
		if err != nil || !ok {
			return "", false
		}
		return v, true
	}
	if v, ok := get(KeyMediaDriver); ok && strings.TrimSpace(v) != "" {
		rt.Driver = strings.TrimSpace(v)
	}
	if v, ok := get(KeyWebDAVURL); ok {
		rt.WebDAVURL = strings.TrimSpace(v)
	}
	if v, ok := get(KeyWebDAVUsername); ok {
		rt.WebDAVUsername = strings.TrimSpace(v)
	}
	if v, ok := get(KeyWebDAVPassword); ok {
		rt.WebDAVPassword = v
	}
	if v, ok := get(KeyWebDAVRoot); ok {
		rt.WebDAVRoot = strings.TrimSpace(v)
	}
	if v, ok := get(KeyMediaLocalFallback); ok {
		rt.LocalFallback = parseMediaBool(v, rt.LocalFallback)
	}
	// local root stays startup-owned
	rt.LocalVideoRoot = c.defaults.LocalVideoRoot
	rt = normalizeMediaRuntime(rt)
	c.mu.Lock()
	c.current = rt
	c.mu.Unlock()
	return nil
}

func (c *MediaConfig) Update(ctx context.Context, in MediaUpdateInput) (MediaRuntime, error) {
	if c == nil {
		return MediaRuntime{}, fmt.Errorf("media config unavailable")
	}
	cur := c.Get()
	next := cur
	if in.Driver != nil {
		next.Driver = strings.TrimSpace(*in.Driver)
	}
	if in.WebDAVURL != nil {
		next.WebDAVURL = strings.TrimSpace(*in.WebDAVURL)
	}
	if in.WebDAVUsername != nil {
		next.WebDAVUsername = strings.TrimSpace(*in.WebDAVUsername)
	}
	if in.PasswordClear != nil && *in.PasswordClear {
		next.WebDAVPassword = ""
	} else if in.WebDAVPassword != nil && strings.TrimSpace(*in.WebDAVPassword) != "" {
		next.WebDAVPassword = strings.TrimSpace(*in.WebDAVPassword)
	}
	if in.WebDAVRoot != nil {
		next.WebDAVRoot = strings.TrimSpace(*in.WebDAVRoot)
	}
	if in.LocalFallback != nil {
		next.LocalFallback = *in.LocalFallback
	}
	next.LocalVideoRoot = c.defaults.LocalVideoRoot
	next = normalizeMediaRuntime(next)
	if err := validateMediaRuntime(next); err != nil {
		return cur, err
	}
	kv := map[string]string{
		KeyMediaDriver:        next.Driver,
		KeyWebDAVURL:          next.WebDAVURL,
		KeyWebDAVUsername:     next.WebDAVUsername,
		KeyWebDAVPassword:     next.WebDAVPassword,
		KeyWebDAVRoot:         next.WebDAVRoot,
		KeyMediaLocalFallback: fmt.Sprintf("%t", next.LocalFallback),
	}
	if err := c.store.SetSettings(ctx, kv); err != nil {
		return cur, err
	}
	c.mu.Lock()
	c.current = next
	c.mu.Unlock()
	return next, nil
}

func (rt MediaRuntime) StoreOptions() MediaSettings {
	fallback := rt.LocalFallback
	return MediaSettings{
		Driver:         rt.Driver,
		LocalVideoRoot: rt.LocalVideoRoot,
		WebDAVURL:      rt.WebDAVURL,
		WebDAVUsername: rt.WebDAVUsername,
		WebDAVPassword: rt.WebDAVPassword,
		WebDAVRoot:     rt.WebDAVRoot,
		LocalFallback:  &fallback,
	}
}

func normalizeMediaRuntime(rt MediaRuntime) MediaRuntime {
	switch strings.ToLower(strings.TrimSpace(rt.Driver)) {
	case MediaDriverWebDAV:
		rt.Driver = MediaDriverWebDAV
	default:
		rt.Driver = MediaDriverLocal
	}
	rt.WebDAVURL = strings.TrimSpace(rt.WebDAVURL)
	rt.WebDAVUsername = strings.TrimSpace(rt.WebDAVUsername)
	rt.WebDAVRoot = strings.Trim(strings.ReplaceAll(strings.TrimSpace(rt.WebDAVRoot), "\\", "/"), "/")
	rt.LocalVideoRoot = strings.TrimSpace(rt.LocalVideoRoot)
	return rt
}

func validateMediaRuntime(rt MediaRuntime) error {
	switch rt.Driver {
	case MediaDriverLocal:
		if strings.TrimSpace(rt.LocalVideoRoot) == "" {
			return fmt.Errorf("本地媒体目录未配置")
		}
		return nil
	case MediaDriverWebDAV:
		if rt.WebDAVURL == "" {
			return fmt.Errorf("WebDAV URL 不能为空")
		}
		if !strings.HasPrefix(strings.ToLower(rt.WebDAVURL), "http://") && !strings.HasPrefix(strings.ToLower(rt.WebDAVURL), "https://") {
			return fmt.Errorf("WebDAV URL 必须以 http:// 或 https:// 开头")
		}
		return nil
	default:
		return fmt.Errorf("不支持的媒体存储驱动 %q", rt.Driver)
	}
}

// ProbeMediaRuntime verifies the backend can write/read/delete a tiny object.
func ProbeMediaRuntime(ctx context.Context, rt MediaRuntime) error {
	rt = normalizeMediaRuntime(rt)
	if err := validateMediaRuntime(rt); err != nil {
		return err
	}
	falseFallback := false
	opts := MediaSettings{
		Driver:         rt.Driver,
		LocalVideoRoot: rt.LocalVideoRoot,
		WebDAVURL:      rt.WebDAVURL,
		WebDAVUsername: rt.WebDAVUsername,
		WebDAVPassword: rt.WebDAVPassword,
		WebDAVRoot:     rt.WebDAVRoot,
		LocalFallback:  &falseFallback,
	}
	ms, err := NewMediaStore(opts)
	if err != nil {
		return err
	}
	// unwrap fallback to primary for local is fine
	if fb, ok := ms.(*fallbackMediaStore); ok {
		ms = fb.primary
	}
	name := fmt.Sprintf("probe-%d.txt", time.Now().UTC().UnixNano())
	payload := []byte("ok")
	if err := ms.Put(ctx, MediaKindImage, name, strings.NewReader(string(payload)), int64(len(payload)), "text/plain"); err != nil {
		return fmt.Errorf("写入探测文件失败: %w", err)
	}
	defer func() { _ = ms.Delete(context.Background(), MediaKindImage, name) }()
	content, err := ms.Open(ctx, MediaKindImage, name, "")
	if err != nil {
		return fmt.Errorf("读取探测文件失败: %w", err)
	}
	got, readErr := io.ReadAll(io.LimitReader(content.Body, 64))
	_ = content.Body.Close()
	if readErr != nil {
		return fmt.Errorf("读取探测文件失败: %w", readErr)
	}
	if string(got) != "ok" {
		return fmt.Errorf("探测文件内容不匹配")
	}
	return nil
}

func MediaDriverOptions() []map[string]string {
	return []map[string]string{
		{"value": MediaDriverLocal, "label": "本地磁盘"},
		{"value": MediaDriverWebDAV, "label": "WebDAV / AList"},
	}
}

func MaskMediaSecret(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if len(s) <= 8 {
		return "****"
	}
	return s[:2] + "****" + s[len(s)-2:]
}

func parseMediaBool(v string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func MediaRuntimeFromEnvConfig(driver, root, url, user, pass, webRoot string, fallback *bool) MediaRuntime {
	rt := MediaRuntime{
		Driver:         driver,
		LocalVideoRoot: root,
		WebDAVURL:      url,
		WebDAVUsername: user,
		WebDAVPassword: pass,
		WebDAVRoot:     webRoot,
		LocalFallback:  true,
	}
	if fallback != nil {
		rt.LocalFallback = *fallback
	}
	return normalizeMediaRuntime(rt)
}
