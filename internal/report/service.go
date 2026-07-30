package report

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"sub2api-ext/internal/notify"
	"sub2api-ext/internal/store"
)

// sendWindow is how long after the configured time a missed report may still
// be delivered. It bounds the damage of a restart: a short outage still gets
// the report, a long one skips the day instead of sending a stale digest at a
// random hour.
const sendWindow = 2 * time.Hour

// tickInterval is the scheduler resolution. Within the send window a failed
// delivery is retried on the next tick.
const tickInterval = 30 * time.Second

// Stats is a delivery counter snapshot for the admin page.
type Stats struct {
	Sent       int64  `json:"sent"`
	Failed     int64  `json:"failed"`
	LastDate   string `json:"last_date,omitempty"`
	LastSentAt string `json:"last_sent_at,omitempty"`
	LastError  string `json:"last_error,omitempty"`
	NextDueAt  string `json:"next_due_at,omitempty"`
}

// Service owns the daily scheduler and manual sends.
type Service struct {
	store    *store.Store
	settings *Settings
	notifier *notify.Notifier
	deps     Deps

	schedMu   sync.Mutex
	schedStop chan struct{}
	schedWG   sync.WaitGroup

	statMu       sync.Mutex
	sent         int64
	failed       int64
	lastDate     string
	lastSentDate string // covered date already delivered (memory cache of KeyLastSent)
	lastSentAt   time.Time
	lastErr      string
}

func NewService(st *store.Store, settings *Settings, notifier *notify.Notifier, deps Deps) *Service {
	return &Service{store: st, settings: settings, notifier: notifier, deps: deps}
}

// Settings exposes the runtime configuration.
func (s *Service) Settings() *Settings { return s.settings }

// StartScheduler begins the daily send loop.
func (s *Service) StartScheduler() {
	s.schedMu.Lock()
	defer s.schedMu.Unlock()
	if s.schedStop != nil {
		return
	}
	s.schedStop = make(chan struct{})
	stop := s.schedStop
	s.schedWG.Add(1)
	go func() {
		defer s.schedWG.Done()
		s.loop(stop)
	}()
	log.Printf("report scheduler started")
}

// StopScheduler stops the send loop.
func (s *Service) StopScheduler() {
	s.schedMu.Lock()
	stop := s.schedStop
	s.schedStop = nil
	s.schedMu.Unlock()
	if stop != nil {
		close(stop)
		s.schedWG.Wait()
	}
}

func (s *Service) loop(stop <-chan struct{}) {
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-ticker.C:
			s.tick(now)
		}
	}
}

func (s *Service) tick(now time.Time) {
	rt := s.settings.Get()
	if !rt.Enabled {
		return
	}
	loc := rt.Location()
	local := now.In(loc)
	if !DueNow(rt, local) {
		return
	}
	date := rt.CoverDate(local)
	if s.alreadySent(date) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := s.deliver(ctx, rt, date, local, "schedule"); err != nil {
		log.Printf("report scheduled send failed: %v", err)
	}
}

// alreadySent reports whether the covered date was already delivered.
// Memory is checked first; SQLite is the source of truth across restarts.
func (s *Service) alreadySent(date string) bool {
	s.statMu.Lock()
	if s.lastSentDate == date {
		s.statMu.Unlock()
		return true
	}
	s.statMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	last, ok, err := s.store.GetSetting(ctx, KeyLastSent)
	if err != nil || !ok || last != date {
		return false
	}
	s.statMu.Lock()
	s.lastSentDate = date
	s.statMu.Unlock()
	return true
}

// DueAt returns today's scheduled moment in the given local time.
func DueAt(rt Runtime, local time.Time) (time.Time, bool) {
	h, m, err := ParseSendAt(rt.SendAt)
	if err != nil {
		return time.Time{}, false
	}
	return time.Date(local.Year(), local.Month(), local.Day(), h, m, 0, 0, local.Location()), true
}

// DueNow reports whether a local moment falls inside today's send window.
func DueNow(rt Runtime, local time.Time) bool {
	due, ok := DueAt(rt, local)
	if !ok {
		return false
	}
	if local.Before(due) {
		return false
	}
	return local.Sub(due) <= sendWindow
}

// NextDue returns the next moment a scheduled report would be produced.
func NextDue(rt Runtime, now time.Time) (time.Time, bool) {
	local := now.In(rt.Location())
	due, ok := DueAt(rt, local)
	if !ok {
		return time.Time{}, false
	}
	if !local.Before(due) {
		due = due.AddDate(0, 0, 1)
	}
	return due, true
}

// Preview builds the digest for the current configuration without sending it.
func (s *Service) Preview(ctx context.Context, now time.Time) (Digest, error) {
	rt := s.settings.Get()
	local := now.In(rt.Location())
	return Build(ctx, s.store, rt, s.deps, rt.CoverDate(local), local)
}

// SendNow builds and delivers the digest immediately. It is synchronous so
// the admin page can surface the real delivery error.
func (s *Service) SendNow(ctx context.Context, now time.Time) (Digest, error) {
	rt := s.settings.Get()
	local := now.In(rt.Location())
	return s.deliver(ctx, rt, rt.CoverDate(local), local, "manual")
}

func (s *Service) deliver(ctx context.Context, rt Runtime, date string, local time.Time, trigger string) (Digest, error) {
	d, err := Build(ctx, s.store, rt, s.deps, date, local)
	if err != nil {
		s.recordFailure(date, err)
		return d, err
	}
	if s.notifier == nil {
		err := fmt.Errorf("通知模块不可用")
		s.recordFailure(date, err)
		return d, err
	}
	nrt := s.notifier.Settings().Get()
	if !nrt.Enabled {
		err := fmt.Errorf("通知中心未开启，日报无法送达")
		s.recordFailure(date, err)
		return d, err
	}
	// Delivered directly rather than through Publish: the report has its own
	// on/off switch and must not be silenced by the alert level floor or by
	// the per-event subscription list.
	if err := s.notifier.Send(ctx, nrt, d.Event()); err != nil {
		s.recordFailure(date, err)
		return d, err
	}
	// Persist de-dupe key only after successful delivery so a failed send can
	// still retry inside the window. Manual and scheduled paths share this.
	if err := s.store.SetSetting(ctx, KeyLastSent, date); err != nil {
		log.Printf("report mark sent failed date=%s: %v", date, err)
	}
	s.statMu.Lock()
	s.sent++
	s.lastDate = date
	s.lastSentDate = date
	s.lastSentAt = time.Now()
	s.lastErr = ""
	s.statMu.Unlock()
	log.Printf("report delivered date=%s trigger=%s", date, trigger)
	return d, nil
}

func (s *Service) recordFailure(date string, err error) {
	s.statMu.Lock()
	s.failed++
	s.lastDate = date
	s.lastErr = err.Error()
	s.statMu.Unlock()
}

// Stats returns a counter snapshot for the admin page.
func (s *Service) Stats() Stats {
	s.statMu.Lock()
	out := Stats{
		Sent:      s.sent,
		Failed:    s.failed,
		LastDate:  s.lastDate,
		LastError: s.lastErr,
	}
	if !s.lastSentAt.IsZero() {
		out.LastSentAt = s.lastSentAt.Format(time.RFC3339)
	}
	s.statMu.Unlock()
	rt := s.settings.Get()
	if next, ok := NextDue(rt, time.Now()); ok && rt.Enabled {
		out.NextDueAt = next.Format("2006-01-02 15:04")
	}
	return out
}
