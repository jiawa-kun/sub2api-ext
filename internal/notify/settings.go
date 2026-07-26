package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"sub2api-ext/internal/config"
	"sub2api-ext/internal/store"
)

// app_settings keys.
const (
	KeyEnabled  = "notify_enabled"
	KeyChannel  = "notify_channel"
	KeyTarget   = "notify_target"
	KeyExtra    = "notify_extra"
	KeySecret   = "notify_secret"
	KeyEvents   = "notify_events"
	KeyMinLevel = "notify_min_level"
)

// Runtime is the hot-reloadable notify configuration.
type Runtime struct {
	Enabled bool   `json:"enabled"`
	Channel string `json:"channel"`
	Target  string `json:"target"`
	// Extra is channel-specific (telegram chat id).
	Extra string `json:"extra"`
	// Secret is sent as a bearer token; never returned in plaintext.
	Secret   string   `json:"-"`
	Events   []string `json:"events"`
	MinLevel string   `json:"min_level"`
}

// Subscribed reports whether an event type should be delivered.
func (r Runtime) Subscribed(eventType string) bool {
	if eventType == TypeTest {
		return true
	}
	for _, e := range r.Events {
		if e == eventType {
			return true
		}
	}
	return false
}

// UpdateInput allows partial updates.
type UpdateInput struct {
	Enabled     *bool     `json:"enabled"`
	Channel     *string   `json:"channel"`
	Target      *string   `json:"target"`
	Extra       *string   `json:"extra"`
	Secret      *string   `json:"secret"`
	SecretClear *bool     `json:"secret_clear"`
	Events      *[]string `json:"events"`
	MinLevel    *string   `json:"min_level"`
}

// Settings holds runtime notify config backed by app_settings.
type Settings struct {
	mu      sync.RWMutex
	store   *store.Store
	current Runtime
}

func NewSettings(st *store.Store, defaults config.NotifyConfig) *Settings {
	s := &Settings{
		store:   st,
		current: normalize(fromConfig(defaults)),
	}
	_ = s.Reload(context.Background())
	return s
}

func fromConfig(c config.NotifyConfig) Runtime {
	return Runtime{
		Enabled:  c.Enabled,
		Channel:  c.Channel,
		Target:   c.Target,
		Extra:    c.Extra,
		Secret:   c.Secret,
		Events:   append([]string{}, c.Events...),
		MinLevel: c.MinLevel,
	}
}

func (s *Settings) Get() Runtime {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return clone(s.current)
}

func (s *Settings) Reload(ctx context.Context) error {
	rt := s.Get()
	get := func(key string) (string, bool) {
		v, ok, err := s.store.GetSetting(ctx, key)
		if err != nil || !ok {
			return "", false
		}
		return v, true
	}
	if v, ok := get(KeyEnabled); ok {
		rt.Enabled = parseBool(v, rt.Enabled)
	}
	if v, ok := get(KeyChannel); ok && strings.TrimSpace(v) != "" {
		rt.Channel = strings.TrimSpace(v)
	}
	if v, ok := get(KeyTarget); ok {
		rt.Target = strings.TrimSpace(v)
	}
	if v, ok := get(KeyExtra); ok {
		rt.Extra = strings.TrimSpace(v)
	}
	if v, ok := get(KeySecret); ok {
		rt.Secret = v
	}
	if v, ok := get(KeyEvents); ok {
		rt.Events = parseEvents(v)
	}
	if v, ok := get(KeyMinLevel); ok && strings.TrimSpace(v) != "" {
		rt.MinLevel = strings.TrimSpace(v)
	}
	rt = normalize(rt)
	s.mu.Lock()
	s.current = rt
	s.mu.Unlock()
	return nil
}

func (s *Settings) Update(ctx context.Context, in UpdateInput) (Runtime, error) {
	cur := s.Get()
	next := cur
	if in.Enabled != nil {
		next.Enabled = *in.Enabled
	}
	if in.Channel != nil {
		next.Channel = strings.TrimSpace(*in.Channel)
	}
	if in.Target != nil {
		next.Target = strings.TrimSpace(*in.Target)
	}
	if in.Extra != nil {
		next.Extra = strings.TrimSpace(*in.Extra)
	}
	if in.SecretClear != nil && *in.SecretClear {
		next.Secret = ""
	} else if in.Secret != nil && strings.TrimSpace(*in.Secret) != "" {
		next.Secret = strings.TrimSpace(*in.Secret)
	}
	if in.Events != nil {
		next.Events = normalizeEvents(*in.Events)
	}
	if in.MinLevel != nil {
		next.MinLevel = strings.TrimSpace(*in.MinLevel)
	}
	next = normalize(next)
	if err := validate(next); err != nil {
		return cur, err
	}

	kv := map[string]string{
		KeyEnabled:  fmt.Sprintf("%t", next.Enabled),
		KeyChannel:  next.Channel,
		KeyTarget:   next.Target,
		KeyExtra:    next.Extra,
		KeySecret:   next.Secret,
		KeyEvents:   eventsToJSON(next.Events),
		KeyMinLevel: next.MinLevel,
	}
	if err := s.store.SetSettings(ctx, kv); err != nil {
		return cur, err
	}
	s.mu.Lock()
	s.current = next
	s.mu.Unlock()
	return clone(next), nil
}

func normalize(rt Runtime) Runtime {
	switch strings.ToLower(strings.TrimSpace(rt.Channel)) {
	case ChannelWeCom, ChannelTelegram, ChannelWebhook:
		rt.Channel = strings.ToLower(strings.TrimSpace(rt.Channel))
	default:
		rt.Channel = ChannelWebhook
	}
	switch strings.ToLower(strings.TrimSpace(rt.MinLevel)) {
	case LevelInfo, LevelWarn, LevelError:
		rt.MinLevel = strings.ToLower(strings.TrimSpace(rt.MinLevel))
	default:
		rt.MinLevel = LevelWarn
	}
	rt.Target = strings.TrimSpace(rt.Target)
	rt.Extra = strings.TrimSpace(rt.Extra)
	if rt.Events == nil {
		rt.Events = []string{}
	}
	rt.Events = normalizeEvents(rt.Events)
	if len(rt.Events) == 0 {
		rt.Events = AllTypes()
	}
	return rt
}

func validate(rt Runtime) error {
	// Only enforce a usable target when notifications are actually on, so an
	// operator can stage the config before switching it live.
	if !rt.Enabled {
		return nil
	}
	if err := ValidateURL(rt.Channel, rt.Target); err != nil {
		return err
	}
	if rt.Channel == ChannelTelegram && strings.TrimSpace(rt.Extra) == "" {
		return fmt.Errorf("telegram 需要填写 chat id")
	}
	return nil
}

func clone(rt Runtime) Runtime {
	out := rt
	out.Events = append([]string{}, rt.Events...)
	return out
}

// normalizeEvents drops unknown/duplicate event types.
func normalizeEvents(in []string) []string {
	known := map[string]struct{}{}
	for _, t := range AllTypes() {
		known[t] = struct{}{}
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, e := range in {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if _, ok := known[e]; !ok {
			continue
		}
		if _, dup := seen[e]; dup {
			continue
		}
		seen[e] = struct{}{}
		out = append(out, e)
	}
	return out
}

func parseEvents(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []string{}
	}
	if strings.HasPrefix(raw, "[") {
		var arr []string
		if err := json.Unmarshal([]byte(raw), &arr); err == nil {
			return normalizeEvents(arr)
		}
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == ';'
	})
	return normalizeEvents(parts)
}

func eventsToJSON(events []string) string {
	b, _ := json.Marshal(normalizeEvents(events))
	return string(b)
}

func parseBool(v string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

// MaskSecret returns a display-safe representation of a credential.
func MaskSecret(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if len(s) <= 8 {
		return "****"
	}
	return s[:4] + "****" + s[len(s)-2:]
}

// MaskTarget hides the path/query of a webhook URL so admin reads do not
// leak the full secret-bearing endpoint.
func MaskTarget(channel, target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return ""
	}
	if channel == ChannelTelegram {
		return MaskSecret(target)
	}
	i := strings.Index(target, "://")
	if i < 0 {
		return MaskSecret(target)
	}
	rest := target[i+3:]
	host := rest
	if j := strings.Index(rest, "/"); j >= 0 {
		host = rest[:j]
	}
	if len(rest) > len(host) {
		return target[:i+3] + host + "/****"
	}
	return target[:i+3] + host
}
