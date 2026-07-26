package notify

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	queueSize      = 256
	sendTimeout    = 10 * time.Second
	maxSendRetries = 2
)

// Notifier asynchronously delivers events to the configured channel.
type Notifier struct {
	settings *Settings
	client   *http.Client

	queue chan Event

	mu      sync.Mutex
	started bool
	stop    chan struct{}
	wg      sync.WaitGroup

	// stats
	statMu   sync.Mutex
	sent     int64
	failed   int64
	dropped  int64
	lastErr  string
	lastSent time.Time
}

func NewNotifier(s *Settings) *Notifier {
	return &Notifier{
		settings: s,
		client:   &http.Client{Timeout: sendTimeout},
		queue:    make(chan Event, queueSize),
	}
}

// Start launches the background delivery worker.
func (n *Notifier) Start() {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.started {
		return
	}
	n.started = true
	n.stop = make(chan struct{})
	stop := n.stop
	n.wg.Add(1)
	go func() {
		defer n.wg.Done()
		n.loop(stop)
	}()
	log.Printf("notify worker started")
}

// Stop shuts the worker down. It waits only briefly: an in-flight HTTP send
// to a hanging endpoint must not delay process shutdown.
func (n *Notifier) Stop() {
	n.mu.Lock()
	stop := n.stop
	n.started = false
	n.stop = nil
	n.mu.Unlock()
	if stop == nil {
		return
	}
	close(stop)
	done := make(chan struct{})
	go func() {
		n.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		log.Printf("notify worker still draining, continuing shutdown")
	}
}

// Settings exposes the runtime configuration.
func (n *Notifier) Settings() *Settings { return n.settings }

// Publish enqueues an event. It never blocks: if the queue is full the event
// is dropped, because notification must never slow down the caller.
func (n *Notifier) Publish(ev Event) {
	if n == nil {
		return
	}
	rt := n.settings.Get()
	if !rt.Enabled {
		return
	}
	if !rt.Subscribed(ev.Type) {
		return
	}
	if levelRank(ev.Level) < levelRank(rt.MinLevel) {
		return
	}
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	select {
	case n.queue <- ev:
	default:
		n.statMu.Lock()
		n.dropped++
		n.statMu.Unlock()
		log.Printf("notify queue full, dropped event %s", ev.Type)
	}
}

func (n *Notifier) loop(stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case ev := <-n.queue:
			n.deliver(stop, ev)
		}
	}
}

func (n *Notifier) deliver(stop <-chan struct{}, ev Event) {
	rt := n.settings.Get()
	// Cancel the in-flight request as soon as shutdown is requested.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-stop:
			cancel()
		case <-ctx.Done():
		}
	}()

	var lastErr error
	for attempt := 0; attempt <= maxSendRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-stop:
				return
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		err := n.Send(ctx, rt, ev)
		if err == nil {
			n.statMu.Lock()
			n.sent++
			n.lastSent = time.Now()
			n.lastErr = ""
			n.statMu.Unlock()
			return
		}
		lastErr = err
	}
	n.statMu.Lock()
	n.failed++
	n.lastErr = lastErr.Error()
	n.statMu.Unlock()
	log.Printf("notify send failed after retries: %v", lastErr)
}

// Send performs one synchronous delivery. Exported so the admin "test"
// endpoint can surface the real error to the operator.
func (n *Notifier) Send(ctx context.Context, rt Runtime, ev Event) error {
	if err := ValidateURL(rt.Channel, rt.Target); err != nil {
		return err
	}
	p, err := render(rt.Channel, rt.Target, rt.Extra, ev)
	if err != nil {
		return err
	}
	if err := ValidateURL(ChannelWebhook, p.URL); err != nil {
		return err
	}

	reqCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, p.URL, bytes.NewReader(p.Body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if s := strings.TrimSpace(rt.Secret); s != "" {
		req.Header.Set("Authorization", "Bearer "+s)
	}

	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	return nil
}

// Stats is a delivery counter snapshot.
type Stats struct {
	Sent     int64  `json:"sent"`
	Failed   int64  `json:"failed"`
	Dropped  int64  `json:"dropped"`
	LastErr  string `json:"last_error,omitempty"`
	LastSent string `json:"last_sent_at,omitempty"`
	Queued   int    `json:"queued"`
}

func (n *Notifier) Stats() Stats {
	n.statMu.Lock()
	defer n.statMu.Unlock()
	out := Stats{
		Sent:    n.sent,
		Failed:  n.failed,
		Dropped: n.dropped,
		LastErr: n.lastErr,
		Queued:  len(n.queue),
	}
	if !n.lastSent.IsZero() {
		out.LastSent = n.lastSent.UTC().Format(time.RFC3339)
	}
	return out
}

// ValidateURL rejects anything that is not a plain http(s) endpoint. For
// telegram the target is a bot token rather than a URL, so it is only
// checked for obvious junk.
func ValidateURL(channel, target string) error {
	target = strings.TrimSpace(target)
	if target == "" {
		return fmt.Errorf("通知地址为空")
	}
	if channel == ChannelTelegram {
		if strings.ContainsAny(target, " \t\r\n") {
			return fmt.Errorf("telegram bot token 格式不正确")
		}
		return nil
	}
	u, err := url.Parse(target)
	if err != nil {
		return fmt.Errorf("通知地址无法解析: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return fmt.Errorf("通知地址必须是 http 或 https")
	}
	if strings.TrimSpace(u.Host) == "" {
		return fmt.Errorf("通知地址缺少主机名")
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
