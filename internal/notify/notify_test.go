package notify_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"sub2api-ext/internal/config"
	"sub2api-ext/internal/notify"
	"sub2api-ext/internal/store"
)

func newSettings(t *testing.T, def config.NotifyConfig) *notify.Settings {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return notify.NewSettings(st, def)
}

func TestWebhookDeliveryPayload(t *testing.T) {
	var got atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.Store(string(b))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := newSettings(t, config.NotifyConfig{})
	enabled := true
	target := srv.URL
	if _, err := s.Update(context.Background(), notify.UpdateInput{
		Enabled: &enabled, Target: &target,
	}); err != nil {
		t.Fatal(err)
	}

	n := notify.NewNotifier(s)
	err := n.Send(context.Background(), s.Get(), notify.Event{
		Type:   notify.TypePatrolAccountAction,
		Level:  notify.LevelWarn,
		Title:  "账号巡检：关闭调度",
		Fields: []notify.Field{{Key: "账号", Value: "#101 acc"}},
		Time:   time.Now(),
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	raw, _ := got.Load().(string)
	var body map[string]any
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	if body["type"] != notify.TypePatrolAccountAction {
		t.Fatalf("type = %v", body["type"])
	}
	if body["level"] != notify.LevelWarn {
		t.Fatalf("level = %v", body["level"])
	}
}

func TestWeComRendersTextMessage(t *testing.T) {
	var got atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.Store(string(b))
	}))
	defer srv.Close()

	s := newSettings(t, config.NotifyConfig{})
	enabled, ch, target := true, notify.ChannelWeCom, srv.URL
	if _, err := s.Update(context.Background(), notify.UpdateInput{
		Enabled: &enabled, Channel: &ch, Target: &target,
	}); err != nil {
		t.Fatal(err)
	}

	n := notify.NewNotifier(s)
	if err := n.Send(context.Background(), s.Get(), notify.Event{
		Type: notify.TypeTest, Level: notify.LevelInfo, Title: "测试",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	var body struct {
		MsgType string `json:"msgtype"`
		Text    struct {
			Content string `json:"content"`
		} `json:"text"`
	}
	raw, _ := got.Load().(string)
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatal(err)
	}
	if body.MsgType != "text" || body.Text.Content == "" {
		t.Fatalf("wecom body = %+v", body)
	}
}

// Events below the configured minimum level must not be delivered.
func TestMinLevelFiltersEvents(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()

	s := newSettings(t, config.NotifyConfig{})
	enabled, target, level := true, srv.URL, notify.LevelError
	if _, err := s.Update(context.Background(), notify.UpdateInput{
		Enabled: &enabled, Target: &target, MinLevel: &level,
	}); err != nil {
		t.Fatal(err)
	}

	n := notify.NewNotifier(s)
	n.Start()
	defer n.Stop()

	// warn < error -> filtered out
	n.Publish(notify.Event{Type: notify.TypePatrolRunFinished, Level: notify.LevelWarn, Title: "warn"})
	time.Sleep(300 * time.Millisecond)
	if got := hits.Load(); got != 0 {
		t.Fatalf("warn event delivered despite min_level=error (hits=%d)", got)
	}

	// error >= error -> delivered
	n.Publish(notify.Event{Type: notify.TypePatrolRunFinished, Level: notify.LevelError, Title: "err"})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && hits.Load() == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("error event hits = %d, want 1", got)
	}
}

// Unsubscribed event types must not be delivered.
func TestEventSubscriptionFilter(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()

	s := newSettings(t, config.NotifyConfig{})
	enabled, target := true, srv.URL
	only := []string{notify.TypeCheckinBudget}
	level := notify.LevelInfo
	if _, err := s.Update(context.Background(), notify.UpdateInput{
		Enabled: &enabled, Target: &target, Events: &only, MinLevel: &level,
	}); err != nil {
		t.Fatal(err)
	}

	n := notify.NewNotifier(s)
	n.Start()
	defer n.Stop()

	n.Publish(notify.Event{Type: notify.TypePatrolRunFinished, Level: notify.LevelError, Title: "x"})
	time.Sleep(300 * time.Millisecond)
	if got := hits.Load(); got != 0 {
		t.Fatalf("unsubscribed event delivered (hits=%d)", got)
	}
}

// A dead endpoint must never block the publisher.
func TestPublishNeverBlocksOnDeadEndpoint(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer srv.Close()
	defer close(block)

	s := newSettings(t, config.NotifyConfig{})
	enabled, target, level := true, srv.URL, notify.LevelInfo
	if _, err := s.Update(context.Background(), notify.UpdateInput{
		Enabled: &enabled, Target: &target, MinLevel: &level,
	}); err != nil {
		t.Fatal(err)
	}

	n := notify.NewNotifier(s)
	n.Start()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			n.Publish(notify.Event{Type: notify.TypePatrolRunFinished, Level: notify.LevelError, Title: "flood"})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked while the webhook endpoint was hanging")
	}
	if n.Stats().Dropped == 0 {
		t.Fatal("expected events to be dropped once the queue filled")
	}

	// Shutdown must not hang on the in-flight request to the stuck endpoint.
	stopped := make(chan struct{})
	go func() { n.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(6 * time.Second):
		t.Fatal("Stop() hung while a send was in flight")
	}
}

func TestDisabledNotifierSendsNothing(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()

	s := newSettings(t, config.NotifyConfig{Target: srv.URL})
	n := notify.NewNotifier(s)
	n.Start()
	defer n.Stop()

	n.Publish(notify.Event{Type: notify.TypePatrolRunFinished, Level: notify.LevelError, Title: "x"})
	time.Sleep(300 * time.Millisecond)
	if got := hits.Load(); got != 0 {
		t.Fatalf("disabled notifier delivered %d events", got)
	}
}

func TestValidateURLRejectsNonHTTP(t *testing.T) {
	for _, bad := range []string{"", "ftp://x/y", "file:///etc/passwd", "javascript:alert(1)", "http://"} {
		if err := notify.ValidateURL(notify.ChannelWebhook, bad); err == nil {
			t.Fatalf("ValidateURL accepted %q", bad)
		}
	}
	if err := notify.ValidateURL(notify.ChannelWebhook, "https://example.com/hook"); err != nil {
		t.Fatalf("valid url rejected: %v", err)
	}
}

// Enabling with an invalid target must fail rather than silently persist.
func TestUpdateRejectsEnableWithBadTarget(t *testing.T) {
	s := newSettings(t, config.NotifyConfig{})
	enabled, target := true, "not-a-url"
	if _, err := s.Update(context.Background(), notify.UpdateInput{
		Enabled: &enabled, Target: &target,
	}); err == nil {
		t.Fatal("enabling with an invalid target should fail")
	}
	if s.Get().Enabled {
		t.Fatal("settings should not have been enabled")
	}
}

func TestMaskingHidesCredentials(t *testing.T) {
	full := "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=SUPER-SECRET-VALUE"
	masked := notify.MaskTarget(notify.ChannelWebhook, full)
	if masked == full {
		t.Fatal("target was not masked")
	}
	if len(masked) > 0 && containsSecret(masked) {
		t.Fatalf("masked target still leaks the key: %s", masked)
	}
	if s := notify.MaskSecret("abcdefghijklmn"); s == "abcdefghijklmn" || s == "" {
		t.Fatalf("secret mask = %s", s)
	}
}

func containsSecret(s string) bool {
	return len(s) >= 12 && (contains(s, "SUPER-SECRET-VALUE") || contains(s, "key="))
}

func contains(h, n string) bool {
	return len(h) >= len(n) && (func() bool {
		for i := 0; i+len(n) <= len(h); i++ {
			if h[i:i+len(n)] == n {
				return true
			}
		}
		return false
	})()
}
