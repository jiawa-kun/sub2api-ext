package patrol_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sub2api-ext/internal/config"
	"sub2api-ext/internal/notify"
	"sub2api-ext/internal/patrol"
	"sub2api-ext/internal/store"
	"sub2api-ext/internal/sub2api"
)

type upstreamCalls struct {
	test    atomic.Int64
	disable atomic.Int64
	deleted atomic.Int64
	// healthy makes the model test succeed, simulating upstream recovery.
	healthy atomic.Bool
}

var (
	reTest        = regexp.MustCompile(`^/api/v1/admin/accounts/(\d+)/test$`)
	reSchedulable = regexp.MustCompile(`^/api/v1/admin/accounts/(\d+)/schedulable$`)
	reAccount     = regexp.MustCompile(`^/api/v1/admin/accounts/(\d+)$`)
)

// newMockUpstream serves a single account whose model test always fails,
// mimicking a flaky upstream provider.
func newMockUpstream(t *testing.T, calls *upstreamCalls) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		if path == "/api/v1/admin/accounts" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"code":0,"message":"ok","data":{"items":[{"id":101,"name":"acc-101","schedulable":true,"platform":"x","type":"t","status":"active","group":"group-a"}],"page":1,"pages":1,"total":1}}`)
			return
		}
		if reTest.MatchString(path) && r.Method == http.MethodPost {
			calls.test.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			if calls.healthy.Load() {
				fmt.Fprint(w, "data: {\"type\":\"test_complete\",\"success\":true}\n\n")
			} else {
				fmt.Fprint(w, "data: {\"type\":\"error\",\"error\":\"upstream 503 simulated\"}\n\n")
			}
			return
		}
		if reSchedulable.MatchString(path) {
			calls.disable.Add(1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"code":0,"message":"ok","data":{}}`)
			return
		}
		if reAccount.MatchString(path) && r.Method == http.MethodDelete {
			calls.deleted.Add(1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"code":0,"message":"ok","data":{}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"code":0,"message":"ok","data":{}}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func runOnce(t *testing.T, svc *patrol.Service) {
	t.Helper()
	if _, err := svc.Trigger(context.Background(), "manual"); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if !svc.Snapshot().Running {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("patrol run did not finish in time")
}

func newService(t *testing.T, baseURL, action string, threshold int) (*patrol.Service, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "patrol.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	client := sub2api.New(baseURL, "admin-test-key", 10*time.Second)
	ps := patrol.NewSettings(st, config.PatrolConfig{
		Enabled:       false,
		Cron:          "0 */6 * * *",
		Groups:        []string{"group-a"},
		TestModel:     "gpt-5.4",
		Concurrency:   1,
		TimeoutMs:     3000,
		ActionOnFail:  action,
		Prompt:        "hi",
		Timezone:      "Asia/Shanghai",
		KeepRuns:      50,
		FailThreshold: threshold,
	})
	return patrol.NewService(client, st, ps), st
}

// A single failure must NOT disable the account when threshold is 2.
func TestPatrolThresholdDefersAction(t *testing.T) {
	calls := &upstreamCalls{}
	up := newMockUpstream(t, calls)
	svc, st := newService(t, up.URL, "disable", 2)

	runOnce(t, svc)

	if got := calls.disable.Load(); got != 0 {
		t.Fatalf("account disabled after 1st failure (calls=%d); threshold gate did not hold", got)
	}
	snap := svc.Snapshot()
	if snap.Stats.Failed != 1 {
		t.Fatalf("failed = %d, want 1", snap.Stats.Failed)
	}
	if snap.Stats.Pending != 1 {
		t.Fatalf("pending = %d, want 1", snap.Stats.Pending)
	}

	rows, err := st.ListPatrolAccountStates(context.Background(), true, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ConsecutiveFail != 1 || rows[0].LastAction != "pending" {
		t.Fatalf("state = %+v", rows)
	}

	// second consecutive failure reaches the threshold -> action applied
	runOnce(t, svc)

	if got := calls.disable.Load(); got != 1 {
		t.Fatalf("disable calls = %d, want 1 after reaching threshold", got)
	}
	snap = svc.Snapshot()
	if snap.Stats.Disabled != 1 {
		t.Fatalf("disabled = %d, want 1", snap.Stats.Disabled)
	}
}

// threshold=1 preserves the legacy behaviour: act on the first failure.
func TestPatrolThresholdOneKeepsLegacyBehaviour(t *testing.T) {
	calls := &upstreamCalls{}
	up := newMockUpstream(t, calls)
	svc, _ := newService(t, up.URL, "disable", 1)

	runOnce(t, svc)

	if got := calls.disable.Load(); got != 1 {
		t.Fatalf("disable calls = %d, want 1", got)
	}
	if snap := svc.Snapshot(); snap.Stats.Disabled != 1 || snap.Stats.Pending != 0 {
		t.Fatalf("stats = %+v", snap.Stats)
	}
}

// delete is the destructive path; it must also respect the threshold.
func TestPatrolThresholdGuardsDelete(t *testing.T) {
	calls := &upstreamCalls{}
	up := newMockUpstream(t, calls)
	svc, _ := newService(t, up.URL, "delete", 3)

	runOnce(t, svc)
	runOnce(t, svc)

	if got := calls.deleted.Load(); got != 0 {
		t.Fatalf("account deleted before threshold (calls=%d)", got)
	}

	runOnce(t, svc)
	if got := calls.deleted.Load(); got != 1 {
		t.Fatalf("delete calls = %d, want 1 at threshold", got)
	}
}

// A transient blip followed by recovery must reset the streak so the account
// is never disabled. This is the core anti-false-positive guarantee.
func TestPatrolRecoveryResetsStreak(t *testing.T) {
	calls := &upstreamCalls{}
	up := newMockUpstream(t, calls)
	svc, st := newService(t, up.URL, "disable", 2)
	ctx := context.Background()

	// run 1: upstream blip
	runOnce(t, svc)
	rows, err := st.ListPatrolAccountStates(ctx, true, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ConsecutiveFail != 1 {
		t.Fatalf("after blip state = %+v", rows)
	}

	// run 2: upstream recovered
	calls.healthy.Store(true)
	runOnce(t, svc)

	if got := calls.disable.Load(); got != 0 {
		t.Fatalf("account was disabled despite recovery (calls=%d)", got)
	}
	rows, err = st.ListPatrolAccountStates(ctx, false, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ConsecutiveFail != 0 || rows[0].LastStatus != "ok" {
		t.Fatalf("after recovery state = %+v", rows)
	}

	// run 3: fails again, but the streak restarts at 1 -> still no action
	calls.healthy.Store(false)
	runOnce(t, svc)
	if got := calls.disable.Load(); got != 0 {
		t.Fatalf("streak was not reset; disable calls = %d", got)
	}
}

// Disabling an account must emit a notification so the operator finds out.
func TestPatrolEmitsNotificationOnAction(t *testing.T) {
	calls := &upstreamCalls{}
	up := newMockUpstream(t, calls)

	var mu sync.Mutex
	var received []string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		received = append(received, string(b))
		mu.Unlock()
	}))
	defer hook.Close()

	svc, st := newService(t, up.URL, "disable", 1)

	ns := notify.NewSettings(st, config.NotifyConfig{})
	enabled, target, level := true, hook.URL, notify.LevelInfo
	if _, err := ns.Update(context.Background(), notify.UpdateInput{
		Enabled: &enabled, Target: &target, MinLevel: &level,
	}); err != nil {
		t.Fatal(err)
	}
	n := notify.NewNotifier(ns)
	n.Start()
	defer n.Stop()
	svc.SetNotifier(n)

	runOnce(t, svc)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		count := len(received)
		mu.Unlock()
		if count >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(received, "\n")
	if !strings.Contains(joined, notify.TypePatrolAccountAction) {
		t.Fatalf("no account-action notification delivered; got: %s", joined)
	}
	if !strings.Contains(joined, notify.TypePatrolRunFinished) {
		t.Fatalf("no run-finished notification delivered; got: %s", joined)
	}
}
