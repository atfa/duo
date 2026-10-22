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

// newObserverTestServer returns a fake OpenAI-compatible endpoint that answers
// with text and counts its requests.
func newObserverTestServer(t *testing.T, text string, requests *atomic.Int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"`+text+`"}}]}`)
	}))
	t.Cleanup(server.Close)
	return server
}

// waitObserverText waits for an observer event carrying text.
func waitObserverText(t *testing.T, sub Subscription, text string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case event := <-sub.Events:
			if event.Kind == events.KindObserver && event.Text == text {
				return
			}
		case <-deadline:
			t.Fatalf("no observation %q arrived", text)
		}
	}
}

func projectObserverSection(t *testing.T, repo string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repo, ".duo", "config.json"))
	if err != nil {
		t.Fatalf("read project config: %v", err)
	}
	var parsed struct {
		Observer map[string]any `json:"observer"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("decode project config: %v\n%s", err, data)
	}
	return parsed.Observer
}

// A human enabling the observer from the interface must take effect in the
// running session, and a later endpoint change must replace it rather than
// leaving two observers reading the same bus.
func TestSetObserverSwitchesTheLiveObserver(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")

	var first, second atomic.Int32
	endpointA := newObserverTestServer(t, "observed by A", &first)
	endpointB := newObserverTestServer(t, "observed by B", &second)

	repo := gitRepo(t)
	opts := headlessOptions(t, repo, t.TempDir(), "127.0.0.1:0")
	opts.Observer = observer.Config{Debounce: 20 * time.Millisecond, Timeout: 2 * time.Second, MaxEvents: 4, MaxTokens: 32}
	svc := New(opts)
	if _, err := svc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer svc.Close()

	sub, unsubscribe := svc.Subscribe(64)
	defer unsubscribe()

	// The key is in the environment for this test, so the view already counts as
	// configured; only Enabled is a setting here.
	if view := svc.View().Observer; view.Enabled {
		t.Fatalf("observer started enabled: %+v", view)
	}

	if err := svc.Do(context.Background(), SetObserver{Enabled: true, Endpoint: endpointA.URL + "/v1", Model: "exec"}); err != nil {
		t.Fatalf("enable observer: %v", err)
	}
	svc.bus.Emit(events.Event{Kind: events.KindAssistant, Agent: protocol.Austin, Text: "first event"})
	waitObserverText(t, sub, "observed by A")
	if got := first.Load(); got != 1 {
		t.Fatalf("endpoint A requests = %d, want 1", got)
	}

	if err := svc.Do(context.Background(), SetObserver{Enabled: true, Endpoint: endpointB.URL + "/v1", Model: "exec"}); err != nil {
		t.Fatalf("switch observer endpoint: %v", err)
	}
	svc.bus.Emit(events.Event{Kind: events.KindAssistant, Agent: protocol.Austin, Text: "second event"})
	waitObserverText(t, sub, "observed by B")
	if got := first.Load(); got != 1 {
		t.Fatalf("the old observer is still running: endpoint A requests = %d", got)
	}

	section := projectObserverSection(t, repo)
	if enabled, _ := section["enabled"].(bool); !enabled || section["endpoint"] != endpointB.URL+"/v1" {
		t.Fatalf("project observer section = %v", section)
	}
	if view := svc.View().Observer; !view.Enabled || view.Endpoint != endpointB.URL+"/v1" || !view.Configured || !view.HasKey {
		t.Fatalf("view after switch = %+v", view)
	}

	if err := svc.Do(context.Background(), SetObserver{Enabled: false, Endpoint: endpointB.URL + "/v1", Model: "exec"}); err != nil {
		t.Fatalf("disable observer: %v", err)
	}
	svc.bus.Emit(events.Event{Kind: events.KindAssistant, Agent: protocol.Austin, Text: "third event"})
	time.Sleep(150 * time.Millisecond)
	if got := second.Load(); got != 1 {
		t.Fatalf("disabled observer still requested the model: endpoint B requests = %d", got)
	}
	if view := svc.View().Observer; view.Enabled {
		t.Fatalf("view still reports an enabled observer: %+v", view)
	}
}

// An invalid or unwritable change must leave the observer that is already
// running exactly as it was.
func TestSetObserverFailureKeepsTheRunningObserver(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")

	var requests atomic.Int32
	endpoint := newObserverTestServer(t, "still observing", &requests)

	repo := gitRepo(t)
	opts := headlessOptions(t, repo, t.TempDir(), "127.0.0.1:0")
	opts.Observer = observer.Config{Debounce: 20 * time.Millisecond, Timeout: 2 * time.Second, MaxEvents: 4, MaxTokens: 32}
	svc := New(opts)
	if _, err := svc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer svc.Close()

	sub, unsubscribe := svc.Subscribe(64)
	defer unsubscribe()
	if err := svc.Do(context.Background(), SetObserver{Enabled: true, Endpoint: endpoint.URL + "/v1", Model: "exec"}); err != nil {
		t.Fatalf("enable observer: %v", err)
	}

	if err := svc.Do(context.Background(), SetObserver{Enabled: true, Endpoint: "", Model: "exec"}); err == nil {
		t.Fatal("expected an invalid endpoint to be rejected")
	}
	svc.bus.Emit(events.Event{Kind: events.KindAssistant, Agent: protocol.Austin, Text: "after invalid attempt"})
	waitObserverText(t, sub, "still observing")

	// Make the project config unwritable so the next change fails at the write.
	configPath := filepath.Join(repo, ".duo", "config.json")
	if err := os.Remove(configPath); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Mkdir(configPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := svc.Do(context.Background(), SetObserver{Enabled: true, Endpoint: endpoint.URL + "/v2", Model: "exec"}); err == nil {
		t.Fatal("expected an unwritable project config to be reported")
	}
	svc.bus.Emit(events.Event{Kind: events.KindAssistant, Agent: protocol.Austin, Text: "after write failure"})
	waitObserverText(t, sub, "still observing")
	if view := svc.View().Observer; view.Endpoint != endpoint.URL+"/v1" {
		t.Fatalf("a failed write changed the live configuration: %+v", view)
	}
}

// The view exposes the setup state a first-run panel needs, and never the key.
func TestObserverViewTracksSetupAndEnvironment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DUO_OBSERVER_API_KEY", "")
	t.Setenv("DUO_OBSERVER", "")

	repo := gitRepo(t)
	opts := headlessOptions(t, repo, t.TempDir(), "127.0.0.1:0")
	svc := New(opts)
	if _, err := svc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer svc.Close()

	if view := svc.View().Observer; view.Configured || view.HasKey || view.KeyFile {
		t.Fatalf("never-configured view = %+v", view)
	}

	path, err := observer.SaveAPIKey("saved-on-this-machine")
	if err != nil {
		t.Fatalf("SaveAPIKey: %v", err)
	}
	view := svc.View().Observer
	if !view.HasKey || !view.KeyFile || !view.Configured {
		t.Fatalf("view after saving a key = %+v", view)
	}
	if strings.Contains(path, "saved-on-this-machine") {
		t.Fatal("the key path must not contain the key")
	}

	t.Setenv("DUO_OBSERVER", "false")
	if view := svc.View().Observer; !view.EnvOverride {
		t.Fatalf("DUO_OBSERVER was not reported as an override: %+v", view)
	}
}

// A config file that carries an observer section is reported as configured even
// when the observer is off: the interface must not offer onboarding twice.
func TestObserverConfiguredOptionReachesTheView(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := gitRepo(t)
	opts := headlessOptions(t, repo, t.TempDir(), "127.0.0.1:0")
	opts.ObserverConfigured = true
	svc := New(opts)
	if _, err := svc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer svc.Close()
	if view := svc.View().Observer; !view.Configured || view.Enabled {
		t.Fatalf("configured-but-off view = %+v", view)
	}
}

// A save from the interface must not defeat an explicit environment override:
// the human's choice is persisted, the environment decides what runs.
func TestSetObserverKeepsEnvironmentOverrides(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")

	var chosenRequests, envRequests atomic.Int32
	chosen := newObserverTestServer(t, "from the saved endpoint", &chosenRequests)
	fromEnv := newObserverTestServer(t, "from the environment endpoint", &envRequests)
	t.Setenv("DUO_OBSERVER", "true")
	t.Setenv("DUO_OBSERVER_ENDPOINT", fromEnv.URL+"/v1")
	t.Setenv("DUO_OBSERVER_MODEL", "exec")

	repo := gitRepo(t)
	opts := headlessOptions(t, repo, t.TempDir(), "127.0.0.1:0")
	opts.Observer = observer.Config{Debounce: 20 * time.Millisecond, Timeout: 2 * time.Second, MaxEvents: 4, MaxTokens: 32}
	svc := New(opts)
	if _, err := svc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer svc.Close()

	sub, unsubscribe := svc.Subscribe(64)
	defer unsubscribe()
	if err := svc.Do(context.Background(), SetObserver{Enabled: false, Endpoint: chosen.URL + "/v1", Model: "saved-model"}); err != nil {
		t.Fatalf("save observer settings: %v", err)
	}

	section := projectObserverSection(t, repo)
	if enabled, _ := section["enabled"].(bool); enabled {
		t.Fatalf("the human's choice was not persisted: %v", section)
	}
	if section["endpoint"] != chosen.URL+"/v1" || section["model"] != "saved-model" {
		t.Fatalf("persisted endpoint/model = %v", section)
	}

	view := svc.View().Observer
	if !view.Enabled || !view.EnvOverride || !view.EnvOverrideEndpoint || !view.EnvOverrideModel {
		t.Fatalf("environment override not visible after save: %+v", view)
	}
	if view.Endpoint != fromEnv.URL+"/v1" || view.Model != "exec" {
		t.Fatalf("view reports the saved values instead of the environment's: %+v", view)
	}

	svc.bus.Emit(events.Event{Kind: events.KindAssistant, Agent: protocol.Austin, Text: "override event"})
	waitObserverText(t, sub, "from the environment endpoint")
	if got := chosenRequests.Load(); got != 0 {
		t.Fatalf("the saved endpoint was used despite the override: %d requests", got)
	}
}

// An existing key file means the observer was set up before, even when the file
// holds nothing usable: onboarding must not reappear, while HasKey must stay
// honest about a key still being missing.
func TestObserverViewTreatsAnEmptyKeyFileAsConfigured(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DUO_OBSERVER_API_KEY", "")
	t.Setenv("DUO_OBSERVER", "")

	path, err := observer.KeyFilePath()
	if err != nil {
		t.Fatalf("KeyFilePath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	repo := gitRepo(t)
	opts := headlessOptions(t, repo, t.TempDir(), "127.0.0.1:0")
	svc := New(opts)
	if _, err := svc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer svc.Close()

	view := svc.View().Observer
	if !view.KeyFile || !view.Configured {
		t.Fatalf("an existing key file must stop onboarding: %+v", view)
	}
	if view.HasKey {
		t.Fatalf("an empty key file must not count as a usable key: %+v", view)
	}
}

// A run-time environment that disables the observer must not let the interface
// save settings that are invalid on their own: the next launch, without that
// variable, would refuse to start.
func TestSetObserverRejectsInvalidSavedSettingsUnderADisablingOverride(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")
	t.Setenv("DUO_OBSERVER", "false")

	repo := gitRepo(t)
	opts := headlessOptions(t, repo, t.TempDir(), "127.0.0.1:0")
	svc := New(opts)
	if _, err := svc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer svc.Close()

	if err := svc.Do(context.Background(), SetObserver{Enabled: true}); err == nil {
		t.Fatal("an enabled observer without endpoint or model was persisted")
	}
	if section := projectObserverSection(t, repo); section != nil {
		if enabled, _ := section["enabled"].(bool); enabled {
			t.Fatalf("invalid settings were persisted: %v", section)
		}
	}

	// The same save is refused from the other side too: an environment override
	// that enables the observer cannot make an empty endpoint runnable.
	t.Setenv("DUO_OBSERVER", "true")
	if err := svc.Do(context.Background(), SetObserver{Enabled: false}); err == nil {
		t.Fatal("an effective configuration without endpoint or model was accepted")
	}
}
