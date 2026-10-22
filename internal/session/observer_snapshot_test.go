package session

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/observer"
	"github.com/atfa/duo/internal/protocol"
)

// snapshotServer answers every report request with the next canned report and
// records the user prompts it was asked with.
type snapshotServer struct {
	mu      sync.Mutex
	prompts []string
	reports []string
}

func newSnapshotServer(t *testing.T, reports ...string) (*snapshotServer, *httptest.Server) {
	t.Helper()
	rec := &snapshotServer{reports: reports}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		rec.mu.Lock()
		prompt := ""
		if len(payload.Messages) > 1 {
			prompt = payload.Messages[1].Content
		}
		rec.prompts = append(rec.prompts, prompt)
		// Canned reports when the test supplied them, otherwise a distinct token
		// per request so a prompt can be checked for the one report it should and
		// should not carry.
		report := fmt.Sprintf("R-%02d", len(rec.prompts))
		if n := len(rec.prompts) - 1; n < len(rec.reports) {
			report = rec.reports[n]
		}
		rec.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"`+report+`"}}]}`)
	}))
	t.Cleanup(server.Close)
	return rec, server
}

func (s *snapshotServer) prompt(t *testing.T, index int) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if index >= len(s.prompts) {
		t.Fatalf("only %d requests were made, wanted #%d", len(s.prompts), index+1)
	}
	return s.prompts[index]
}

func (s *snapshotServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.prompts)
}

// snapshotSession starts a session whose observer talks to rec.
func snapshotSession(t *testing.T, rec *snapshotServer, server *httptest.Server) *Service {
	t.Helper()
	repo := gitRepo(t)
	opts := headlessOptions(t, repo, t.TempDir(), "127.0.0.1:0")
	opts.Observer = observer.Config{
		Enabled: true, Endpoint: server.URL + "/v1", Model: "exec",
		MaxEvents: 4, Timeout: time.Second,
	}
	svc := New(opts)
	if _, err := svc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { svc.Close() })
	return svc
}

// requestSnapshot asks for one report and waits for the model request to finish,
// which keeps working past the three-report history cap.
func requestSnapshot(t *testing.T, svc *Service, rec *snapshotServer) {
	t.Helper()
	before := rec.count()
	svc.RequestObserverReport()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if rec.count() > before && !svc.View().Observer.Analyzing {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("no snapshot request completed; notice=%q", svc.View().Observer.LastNotice)
}

// waitSnapshotPrompt requests reports until one carries want. The observer
// collects the bus on its own goroutine, so a single request could otherwise race
// the events the test just emitted.
func waitSnapshotPrompt(t *testing.T, svc *Service, rec *snapshotServer, want string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		requestSnapshot(t, svc, rec)
		rec.mu.Lock()
		prompt := rec.prompts[len(rec.prompts)-1]
		rec.mu.Unlock()
		if strings.Contains(prompt, want) {
			return prompt
		}
	}
	t.Fatalf("no snapshot carried %q", want)
	return ""
}

// TestSnapshotCarriesLogAndLiveActivity is the human's question end to end: one
// Ctrl+E request has to answer "what did they just do, what are they doing now,
// what next". The recent log is where a stated next step lives, and the live
// block is the only evidence of work in progress.
func TestSnapshotCarriesLogAndLiveActivity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")
	rec, server := newSnapshotServer(t, "report")
	svc := snapshotSession(t, rec, server)

	svc.bus.Emit(events.Event{
		Kind: events.KindPeer, Agent: protocol.Austin, Peer: protocol.Tony,
		Text: "I will finish the request builder; you take the panel next",
	})
	svc.tracker.Handle(protocol.Austin, protocol.ActivityStream)
	svc.tracker.Note(protocol.Austin, protocol.ActivityStream, "", "I am rewriting the prompt builder")
	svc.tracker.Handle(protocol.Tony, protocol.ActivityToolStart)
	svc.tracker.Note(protocol.Tony, protocol.ActivityToolStart, "bash", "bun test")

	prompt := waitSnapshotPrompt(t, svc, rec, "I will finish the request builder")
	for _, want := range []string{
		"Current state",
		"Austin: busy",
		"currently writing: I am rewriting the prompt builder",
		"Tony: busy running tool bash(bun test)",
		"Austin -> Tony [peer]: I will finish the request builder; you take the panel next",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("one snapshot is missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "[activity]") {
		t.Errorf("an empty activity event reached the snapshot:\n%s", prompt)
	}
}

// TestConsecutiveSnapshotsDoNotAccumulateHistory pins the fix for the report the
// human complained about: the old observer merged every report into an
// ever-growing list of finished items. A request now reads only the newest
// report as continuity, and the log window stays bounded.
func TestConsecutiveSnapshotsDoNotAccumulateHistory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")
	rec, server := newSnapshotServer(t)
	svc := snapshotSession(t, rec, server)

	for i := 1; i <= 3; i++ {
		svc.bus.Emit(events.Event{
			Kind: events.KindAssistant, Agent: protocol.Austin,
			Text: fmt.Sprintf("step-%d finished", i),
		})
	}
	// The first request that sees the whole window is only a warm-up: the
	// assertions below are about the request that follows it.
	waitSnapshotPrompt(t, svc, rec, "step-3 finished")

	svc.bus.Emit(events.Event{Kind: events.KindAssistant, Agent: protocol.Tony, Text: "step-4 started"})
	next := waitSnapshotPrompt(t, svc, rec, "step-4 started")
	index := rec.count() - 1
	request := index + 1

	if got := strings.Count(next, "Previous report"); got != 1 {
		t.Fatalf("the request carries %d previous-report blocks, want 1:\n%s", got, next)
	}
	previous := fmt.Sprintf("R-%02d", request-1)
	if request > 1 {
		if got := strings.Count(next, previous); got != 1 {
			t.Fatalf("the newest previous report %q appears %d times, want once:\n%s", previous, got, next)
		}
	}
	own := fmt.Sprintf("R-%02d", request)
	if strings.Contains(next, own) {
		t.Fatalf("the answer %q was fed back as its own continuity:\n%s", own, next)
	}
	for earlier := 1; earlier < request-1; earlier++ {
		if stale := fmt.Sprintf("R-%02d", earlier); strings.Contains(next, stale) {
			t.Fatalf("the request accumulated the older report %q:\n%s", stale, next)
		}
	}
	for _, want := range []string{"step-1 finished", "step-2 finished", "step-3 finished", "step-4 started"} {
		if got := strings.Count(next, want); got != 1 {
			t.Fatalf("%q appears %d times in the request, want once:\n%s", want, got, next)
		}
	}
	second := svc.View().Observer
	if want := min(3, request); len(second.Reports) != want {
		t.Fatalf("panel history = %d reports, want %d", len(second.Reports), want)
	}
	if second.Reports[0].Text != own {
		t.Fatalf("panel history is not newest first: %+v", second.Reports)
	}
}
