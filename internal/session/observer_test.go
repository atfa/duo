package session

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	svc.RequestObserverReport()
	time.Sleep(100 * time.Millisecond)
	if got := requests.Load(); got != 0 {
		t.Fatalf("disabled observer made %d model requests, want 0", got)
	}
	if reports, analyzing, _ := svc.observerReportSnapshot(); len(reports) != 0 || analyzing {
		t.Fatalf("disabled observer reported state: %v %v", reports, analyzing)
	}
}

func TestRequestedReportReachesTheViewAndNotTheTimeline(t *testing.T) {
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")
	var requests atomic.Int32
	server := newObserverTestServer(t, "**Doing now**\\n- Austin is running go test", &requests)

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
	defer svc.Close()

	sub, unsubscribe := svc.Subscribe(32)
	defer unsubscribe()
	svc.bus.Emit(events.Event{Kind: events.KindAssistant, Agent: protocol.Austin, Text: "implementation done"})

	view := requestObserver(t, svc)
	if len(view.Reports) != 1 || view.Reports[0].Text != "**Doing now**\n- Austin is running go test" || view.Reports[0].At.IsZero() {
		t.Fatalf("view reports = %+v", view.Reports)
	}
	if view.Analyzing {
		t.Fatalf("a finished request left the panel analyzing: %+v", view)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("model requests = %d, want 1", got)
	}

	// The report is panel state. A Timeline entry would bury it among the work
	// the human is actually reading, which is what this change removed.
	deadline := time.After(200 * time.Millisecond)
	for {
		select {
		case event := <-sub.Events:
			if event.Kind == events.KindObserver {
				t.Fatalf("observer text reached the session bus: %+v", event)
			}
		case <-deadline:
			return
		}
	}
}

// A queued request must describe the work in progress when it runs, not when the
// human pressed the key.
func TestRequestedReportCarriesLiveActivity(t *testing.T) {
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")

	var (
		mu     sync.Mutex
		prompt string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		mu.Lock()
		prompt = payload.Messages[1].Content
		mu.Unlock()
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"report"}}]}`)
	}))
	defer server.Close()

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
	defer svc.Close()

	svc.tracker.Handle(protocol.Austin, protocol.ActivityToolStart)
	svc.tracker.Note(protocol.Austin, protocol.ActivityToolStart, "bash", "go test ./...")
	svc.tracker.Note(protocol.Austin, protocol.ActivityStream, "", "the observer needs the live block")

	requestObserver(t, svc)

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(prompt, "Current state") {
		t.Fatalf("prompt has no live block:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Austin: busy running tool bash(go test ./...)") {
		t.Fatalf("live block lost the running tool:\n%s", prompt)
	}
	if !strings.Contains(prompt, "currently writing: the observer needs the live block") {
		t.Fatalf("live block lost the in-progress text:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Tony: idle") {
		t.Fatalf("live block did not describe the idle agent:\n%s", prompt)
	}
}

func TestRequestedReportKeepsANewestFirstHistoryOfThree(t *testing.T) {
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")
	var (
		mu    sync.Mutex
		count int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		count++
		n := count
		mu.Unlock()
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"report `+fmt.Sprint(n)+`"}}]}`)
	}))
	defer server.Close()

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
	defer svc.Close()

	for i := 1; i <= 4; i++ {
		want := fmt.Sprintf("report %d", i)
		svc.RequestObserverReport()
		waitFor(t, 3*time.Second, func() bool {
			reports, analyzing, _ := svc.observerReportSnapshot()
			return !analyzing && len(reports) > 0 && reports[0].Text == want
		})
	}
	reports, _, _ := svc.observerReportSnapshot()
	if len(reports) != observerReportsKept {
		t.Fatalf("kept %d reports, want %d", len(reports), observerReportsKept)
	}
	if reports[0].Text != "report 4" || reports[1].Text != "report 3" || reports[2].Text != "report 2" {
		t.Fatalf("reports are not newest first: %+v", reports)
	}
}

// Pressing the key while a request is in flight may queue one re-run, never a
// burst of model calls.
func TestRepeatedRequestsCollapseToOneQueuedRerun(t *testing.T) {
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")
	release := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		<-release
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"report"}}]}`)
	}))
	defer server.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()

	repo := gitRepo(t)
	opts := headlessOptions(t, repo, t.TempDir(), "127.0.0.1:0")
	opts.Observer = observer.Config{
		Enabled: true, Endpoint: server.URL + "/v1", Model: "exec",
		MaxEvents: 4, Timeout: 5 * time.Second,
	}
	svc := New(opts)
	if _, err := svc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer svc.Close()

	svc.RequestObserverReport()
	waitFor(t, time.Second, func() bool { return svc.View().Observer.Analyzing })
	for i := 0; i < 5; i++ {
		svc.RequestObserverReport()
	}
	close(release)
	waitFor(t, 2*time.Second, func() bool {
		reports, analyzing, _ := svc.observerReportSnapshot()
		return !analyzing && len(reports) == 2
	})
	if got := requests.Load(); got != 2 {
		t.Fatalf("model requests = %d, want 2 (one in flight plus one queued re-run)", got)
	}
}

func TestObserverNoticeReachesViewAndClearsOnReconfigureAndReport(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")
	var failures, successes atomic.Int32
	badEndpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		failures.Add(1)
		http.Error(w, "invalid model name", http.StatusBadRequest)
	}))
	defer badEndpoint.Close()
	goodEndpoint := newObserverTestServer(t, "useful progress report", &successes)

	repo := gitRepo(t)
	opts := headlessOptions(t, repo, t.TempDir(), "127.0.0.1:0")
	opts.Observer = observer.Config{
		Enabled: true, Endpoint: badEndpoint.URL + "/v1", Model: "wrong-model",
		MaxEvents: 4, Timeout: time.Second,
	}
	svc := New(opts)
	if _, err := svc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer svc.Close()

	svc.RequestObserverReport()
	waitFor(t, time.Second, func() bool { return svc.View().Observer.LastNotice != "" })
	failed := svc.View().Observer
	if !strings.Contains(failed.LastNotice, "invalid model name") || failed.LastNoticeAt.IsZero() {
		t.Fatalf("observer diagnostic = %+v", failed)
	}
	if failed.Analyzing {
		t.Fatalf("a failed request left the panel analyzing: %+v", failed)
	}

	if err := svc.Do(context.Background(), SetObserver{Enabled: true, Endpoint: goodEndpoint.URL + "/v1", Model: "exec"}); err != nil {
		t.Fatalf("reconfigure observer: %v", err)
	}
	if got := svc.View().Observer; got.LastNotice != "" || !got.LastNoticeAt.IsZero() {
		t.Fatalf("diagnostic remained after reconfiguration: %+v", got)
	}

	view := requestObserver(t, svc)
	if got := view.LastNotice; got != "" || !view.LastNoticeAt.IsZero() {
		t.Fatalf("diagnostic remained after successful report: %+v", got)
	}
	if failures.Load() != 1 || successes.Load() != 1 {
		t.Fatalf("requests failed=%d succeeded=%d, want 1 each", failures.Load(), successes.Load())
	}
}

func TestTruncatedObserverReportRemainsVisibleWithDiagnostic(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"**Just finished**\n- partial"},"finish_reason":"length"}]}`)
	}))
	defer server.Close()

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
	defer svc.Close()

	view := requestObserver(t, svc)
	if view.Reports[0].Text != "**Just finished**\n- partial" {
		t.Fatalf("truncated report = %+v", view.Reports)
	}
	waitFor(t, time.Second, func() bool {
		return strings.Contains(svc.View().Observer.LastNotice, "report truncated at maxTokens")
	})
	if got := svc.View().Observer.LastNotice; !strings.Contains(got, "raise observer.maxTokens") {
		t.Fatalf("truncation diagnostic = %q", got)
	}
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition did not become true before timeout")
}

// requestObserver asks for one report and waits for it to reach the view.
func requestObserver(t *testing.T, svc *Service) ObserverView {
	t.Helper()
	before := len(svc.View().Observer.Reports)
	svc.RequestObserverReport()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		view := svc.View().Observer
		if len(view.Reports) > before && !view.Analyzing {
			return view
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("no observer report arrived; notice=%q", svc.View().Observer.LastNotice)
	return ObserverView{}
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
	opts.Observer = observer.Config{Timeout: 2 * time.Second, MaxEvents: 4, MaxTokens: 32}
	svc := New(opts)
	if _, err := svc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer svc.Close()

	// The key is in the environment for this test, so the view already counts as
	// configured; only Enabled is a setting here.
	if view := svc.View().Observer; view.Enabled {
		t.Fatalf("observer started enabled: %+v", view)
	}

	if err := svc.Do(context.Background(), SetObserver{Enabled: true, Endpoint: endpointA.URL + "/v1", Model: "exec"}); err != nil {
		t.Fatalf("enable observer: %v", err)
	}
	if view := requestObserver(t, svc); view.Reports[0].Text != "observed by A" {
		t.Fatalf("report from A = %+v", view.Reports)
	}
	if got := first.Load(); got != 1 {
		t.Fatalf("endpoint A requests = %d, want 1", got)
	}

	if err := svc.Do(context.Background(), SetObserver{Enabled: true, Endpoint: endpointB.URL + "/v1", Model: "exec"}); err != nil {
		t.Fatalf("switch observer endpoint: %v", err)
	}
	if view := requestObserver(t, svc); view.Reports[0].Text != "observed by B" {
		t.Fatalf("report from B = %+v", view.Reports)
	}
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
	svc.RequestObserverReport()
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
	opts.Observer = observer.Config{Timeout: 2 * time.Second, MaxEvents: 4, MaxTokens: 32}
	svc := New(opts)
	if _, err := svc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer svc.Close()

	if err := svc.Do(context.Background(), SetObserver{Enabled: true, Endpoint: endpoint.URL + "/v1", Model: "exec"}); err != nil {
		t.Fatalf("enable observer: %v", err)
	}

	if err := svc.Do(context.Background(), SetObserver{Enabled: true, Endpoint: "", Model: "exec"}); err == nil {
		t.Fatal("expected an invalid endpoint to be rejected")
	}
	if view := requestObserver(t, svc); view.Reports[0].Text != "still observing" {
		t.Fatalf("report after an invalid attempt = %+v", view.Reports)
	}

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
	requestObserver(t, svc)
	if requests.Load() != 2 {
		t.Fatalf("running observer request count = %d, want 2", requests.Load())
	}
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
	opts.Observer = observer.Config{Timeout: 2 * time.Second, MaxEvents: 4, MaxTokens: 32}
	svc := New(opts)
	if _, err := svc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer svc.Close()

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

	if got := requestObserver(t, svc); got.Reports[0].Text != "from the environment endpoint" {
		t.Fatalf("report came from the wrong endpoint: %+v", got.Reports)
	}
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

// The observer writes its report for the human, so the request prompt has to
// follow the session language and the saved configuration must not accumulate
// instructions when the observer is rebuilt.
func TestObserverPromptFollowsSessionLanguage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")

	prompts := make(chan string, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		for _, message := range body.Messages {
			if message.Role == "system" {
				select {
				case prompts <- message.Content:
				default:
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"report"}}]}`)
	}))
	defer server.Close()

	repo := gitRepo(t)
	opts := headlessOptions(t, repo, t.TempDir(), "127.0.0.1:0")
	opts.Language = "zh"
	opts.Observer = observer.Config{
		Enabled: true, Endpoint: server.URL + "/v1", Model: "exec",
		MaxEvents: 4, Timeout: time.Second,
	}
	svc := New(opts)
	if _, err := svc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer svc.Close()

	requestObserver(t, svc)
	if got := nextObserverPrompt(t, prompts); !strings.Contains(got, "Simplified Chinese") || !strings.Contains(got, "刚完成、现在正在做、接下来、需要你决定") {
		t.Fatalf("zh system prompt = %q, want Simplified Chinese headings", got)
	}

	if err := svc.Do(context.Background(), SetLanguage{Language: "en"}); err != nil {
		t.Fatalf("switch language: %v", err)
	}
	requestObserver(t, svc)
	if got := nextObserverPrompt(t, prompts); strings.Contains(got, "Simplified Chinese") {
		t.Fatalf("en system prompt kept the Chinese instruction: %q", got)
	}

	// Rebuilding the observer must not stack the instruction into the saved
	// settings, so switching back yields exactly one copy.
	if err := svc.Do(context.Background(), SetLanguage{Language: "zh"}); err != nil {
		t.Fatalf("switch language back: %v", err)
	}
	if err := svc.Do(context.Background(), SetObserver{
		Enabled: true, Endpoint: server.URL + "/v1", Model: "exec",
	}); err != nil {
		t.Fatalf("reconfigure observer: %v", err)
	}
	requestObserver(t, svc)
	if got := nextObserverPrompt(t, prompts); strings.Count(got, "Simplified Chinese") != 1 {
		t.Fatalf("rebuilt zh system prompt = %q, want one Chinese instruction", got)
	}
	if saved := svc.opts.Observer.SystemPrompt; strings.Contains(saved, "Simplified Chinese") {
		t.Fatalf("saved observer prompt was mutated: %q", saved)
	}
}

func nextObserverPrompt(t *testing.T, prompts <-chan string) string {
	t.Helper()
	select {
	case prompt := <-prompts:
		return prompt
	case <-time.After(3 * time.Second):
		t.Fatal("no observer request was recorded")
		return ""
	}
}
