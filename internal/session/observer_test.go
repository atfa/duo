package session

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/observer"
	"github.com/atfa/duo/internal/protocol"
)

func TestObserverDisabledLeavesEventsAloneAndMakesNoRequest(t *testing.T) {
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"unexpected"}}]}`)
	}))
	defer server.Close()

	repo := gitRepo(t)
	opts := headlessOptions(t, repo, t.TempDir(), "127.0.0.1:0")
	opts.Observer = observer.Config{Endpoint: server.URL + "/v1", Model: "exec"}
	svc := New(opts)
	if _, err := svc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer svc.Close()

	sub, unsubscribe := svc.Subscribe(32)
	defer unsubscribe()
	svc.bus.Emit(events.Event{Kind: events.KindAssistant, Agent: protocol.Austin, Text: "ordinary event"})
	select {
	case event := <-sub.Events:
		if event.Kind != events.KindAssistant || event.Text != "ordinary event" {
			t.Fatalf("disabled observer changed event: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("ordinary session event was not delivered")
	}
	time.Sleep(100 * time.Millisecond)
	if got := requests.Load(); got != 0 {
		t.Fatalf("disabled observer made %d model requests, want 0", got)
	}
}

func TestEnabledObserverPublishesObservationOnSessionBus(t *testing.T) {
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"Check the unverified test claim."}}]}`)
	}))
	defer server.Close()

	repo := gitRepo(t)
	opts := headlessOptions(t, repo, t.TempDir(), "127.0.0.1:0")
	opts.Observer = observer.Config{
		Enabled: true, Endpoint: server.URL + "/v1", Model: "exec",
		MaxEvents: 4, Debounce: 10 * time.Millisecond, Timeout: time.Second,
	}
	svc := New(opts)
	if _, err := svc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer svc.Close()

	sub, unsubscribe := svc.Subscribe(32)
	defer unsubscribe()
	svc.bus.Emit(events.Event{Kind: events.KindAssistant, Agent: protocol.Austin, Text: "implementation done"})
	deadline := time.After(2 * time.Second)
	for {
		select {
		case event := <-sub.Events:
			if event.Kind == events.KindObserver {
				if event.Agent != protocol.Duo || event.Text != "Check the unverified test claim." {
					t.Fatalf("observer event = %+v", event)
				}
				if got := requests.Load(); got != 1 {
					t.Fatalf("model requests = %d, want 1", got)
				}
				return
			}
		case <-deadline:
			t.Fatalf("no observer event; model requests = %d", requests.Load())
		}
	}
}

func TestInvalidDirectObserverConfigDoesNotPreventSessionStart(t *testing.T) {
	repo := gitRepo(t)
	opts := headlessOptions(t, repo, t.TempDir(), "127.0.0.1:0")
	opts.Observer.Enabled = true
	svc := New(opts)
	if _, err := svc.Start(context.Background()); err != nil {
		t.Fatalf("observer configuration failure prevented session start: %v", err)
	}
	defer svc.Close()
	if svc.observer != nil {
		t.Fatal("invalid observer configuration should be logged and skipped")
	}
}
