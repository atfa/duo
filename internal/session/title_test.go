package session

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/atfa/duo/internal/observer"
)

// titleServer answers every model request with one canned title and counts the
// requests, so a test can tell a naming call from a report.
func titleServer(t *testing.T, content string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"`+content+`"}}]}`)
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

// waitSessionTitle blocks until the persisted snapshot carries a title.
func waitSessionTitle(t *testing.T, svc *Service, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snap, err := svc.store.Load()
		if err == nil && snap.Title == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	snap, err := svc.store.Load()
	t.Fatalf("persisted title = %q (err %v), want %q", snap.Title, err, want)
}

// TestSubmittedTaskNamesTheSessionOnce is the feature's promise: the first task a
// human submits gives the session a name from the observer, the name is part of the
// durable record, and a later task does not spend another model call on it.
func TestSubmittedTaskNamesTheSessionOnce(t *testing.T) {
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")
	server, requests := titleServer(t, "Add session titles")

	repo := gitRepo(t)
	opts := headlessOptions(t, repo, t.TempDir(), "127.0.0.1:0")
	opts.Observer = observer.Config{
		Enabled: true, Endpoint: server.URL + "/v1", Model: "exec",
		MaxEvents: 4, Timeout: 2 * time.Second,
	}
	svc := New(opts)
	if _, err := svc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer svc.Close()

	if err := svc.Do(context.Background(), SubmitTask{Text: "give sessions a title"}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitSessionTitle(t, svc, "Add session titles")
	if got := requests.Load(); got != 1 {
		t.Fatalf("model requests = %d, want exactly one naming call", got)
	}

	// A second round is the same session, so its name does not change.
	if err := svc.Do(context.Background(), SubmitTask{Text: "now something else entirely"}); err != nil {
		t.Fatalf("second submit: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := requests.Load(); got != 1 {
		t.Fatalf("model requests after a second task = %d, want 1", got)
	}
	waitSessionTitle(t, svc, "Add session titles")
}

// TestSessionTitleFailureLeavesIdDisplayed keeps naming advisory: when the model
// cannot name the session, the record simply has no title and every list falls
// back to the id, with nothing on the timeline and no observer notice.
func TestSessionTitleFailureLeavesIdDisplayed(t *testing.T) {
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no model today", http.StatusInternalServerError)
	}))
	defer server.Close()

	repo := gitRepo(t)
	opts := headlessOptions(t, repo, t.TempDir(), "127.0.0.1:0")
	opts.Observer = observer.Config{
		Enabled: true, Endpoint: server.URL + "/v1", Model: "exec",
		MaxEvents: 4, Timeout: 2 * time.Second,
	}
	svc := New(opts)
	if _, err := svc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer svc.Close()

	if err := svc.Do(context.Background(), SubmitTask{Text: "give sessions a title"}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	snap, err := svc.store.Load()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if strings.TrimSpace(snap.Title) != "" {
		t.Fatalf("persisted title = %q after a failed naming call", snap.Title)
	}
	if notice := svc.View().Observer.LastNotice; strings.Contains(notice, "title") {
		t.Fatalf("a failed naming call became an observer notice: %q", notice)
	}
}

// TestNoObserverMeansNoNamingCall pins the trigger condition: without an observer
// model configured, submitting a task must not call any model.
func TestNoObserverMeansNoNamingCall(t *testing.T) {
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")
	server, requests := titleServer(t, "unexpected")

	repo := gitRepo(t)
	opts := headlessOptions(t, repo, t.TempDir(), "127.0.0.1:0")
	opts.Observer = observer.Config{Endpoint: server.URL + "/v1", Model: "exec"}
	svc := New(opts)
	if _, err := svc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer svc.Close()

	if err := svc.Do(context.Background(), SubmitTask{Text: "give sessions a title"}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if got := requests.Load(); got != 0 {
		t.Fatalf("disabled observer made %d requests, want 0", got)
	}
	snap, err := svc.store.Load()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if snap.Title != "" {
		t.Fatalf("persisted title = %q with no observer configured", snap.Title)
	}
}
