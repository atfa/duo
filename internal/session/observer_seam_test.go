package session

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/observer"
	"github.com/atfa/duo/internal/protocol"
)

func TestSetObserverChangesLiveEndpointAndPersistsWithoutSecret(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-never-persist")
	t.Setenv("DUO_OBSERVER", "")
	t.Setenv("DUO_OBSERVER_ENDPOINT", "")
	t.Setenv("DUO_OBSERVER_MODEL", "")

	var firstRequests, secondRequests atomic.Int32
	first := austinObserverTestServer(t, "from first", &firstRequests)
	second := austinObserverTestServer(t, "from second", &secondRequests)
	repo := gitRepo(t)
	opts := headlessOptions(t, repo, t.TempDir(), "127.0.0.1:0")
	opts.Observer = observer.Config{Debounce: 15 * time.Millisecond, Timeout: time.Second, MaxEvents: 4, MaxTokens: 32}
	svc := New(opts)
	if _, err := svc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	sub, unsubscribe := svc.Subscribe(64)
	defer unsubscribe()

	if err := svc.Do(context.Background(), SetObserver{Enabled: true, Endpoint: first.URL + "/v1", Model: "exec"}); err != nil {
		t.Fatal(err)
	}
	svc.bus.Emit(events.Event{Kind: events.KindAssistant, Agent: protocol.Austin, Text: "first burst"})
	waitAustinObserverText(t, sub, "from first")
	if got := firstRequests.Load(); got != 1 {
		t.Fatalf("first endpoint requests = %d", got)
	}

	if err := svc.Do(context.Background(), SetObserver{Enabled: true, Endpoint: second.URL + "/v1", Model: "exec"}); err != nil {
		t.Fatal(err)
	}
	svc.bus.Emit(events.Event{Kind: events.KindAssistant, Agent: protocol.Austin, Text: "second burst"})
	waitAustinObserverText(t, sub, "from second")
	if got := firstRequests.Load(); got != 1 {
		t.Fatalf("old endpoint still active: %d requests", got)
	}
	if got := secondRequests.Load(); got != 1 {
		t.Fatalf("second endpoint requests = %d", got)
	}

	configPath := filepath.Join(repo, ".duo", "config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "sk-never-persist") {
		t.Fatalf("API key was persisted: %s", data)
	}
	var persisted struct {
		Observer observer.Settings `json:"observer"`
	}
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if !persisted.Observer.Enabled || persisted.Observer.Endpoint != second.URL+"/v1" || persisted.Observer.Model != "exec" {
		t.Fatalf("persisted observer = %+v", persisted.Observer)
	}
	viewData, err := json.Marshal(svc.View())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(viewData), "sk-never-persist") {
		t.Fatal("session View exposed the API key")
	}
	if v := svc.View().Observer; !v.Enabled || !v.Configured || !v.HasKey || v.Endpoint != second.URL+"/v1" {
		t.Fatalf("observer view = %+v", v)
	}

	if err := svc.Do(context.Background(), SetObserver{Enabled: false, Endpoint: second.URL + "/v1", Model: "exec"}); err != nil {
		t.Fatal(err)
	}
	svc.bus.Emit(events.Event{Kind: events.KindAssistant, Agent: protocol.Austin, Text: "after disable"})
	time.Sleep(100 * time.Millisecond)
	if got := secondRequests.Load(); got != 1 {
		t.Fatalf("disabled observer made another request: %d", got)
	}
}

func austinObserverTestServer(t *testing.T, reply string, requests *atomic.Int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"`+reply+`"}}]}`)
	}))
	t.Cleanup(server.Close)
	return server
}

func waitAustinObserverText(t *testing.T, sub Subscription, want string) {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-sub.Events:
			if event.Kind == events.KindObserver && event.Text == want {
				return
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for observer text %q", want)
		}
	}
}
