package observer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/protocol"
)

// recorder collects what an observer emits and logs, from any goroutine.
type recorder struct {
	mu      sync.Mutex
	emits   []string
	logs    []string
	emitted chan struct{}
}

func newRecorder() *recorder {
	return &recorder{emitted: make(chan struct{}, 8)}
}

func (r *recorder) emit(text string) {
	r.mu.Lock()
	r.emits = append(r.emits, text)
	r.mu.Unlock()
	select {
	case r.emitted <- struct{}{}:
	default:
	}
}

func (r *recorder) logf(format string, args ...any) {
	r.mu.Lock()
	r.logs = append(r.logs, fmt.Sprintf(format, args...))
	r.mu.Unlock()
}

func (r *recorder) emittedText() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.emits...)
}

func (r *recorder) logged() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.logs, "\n")
}

// waitEmit fails the test unless an observation arrives before the deadline.
func waitEmit(t *testing.T, r *recorder) string {
	t.Helper()
	select {
	case <-r.emitted:
		texts := r.emittedText()
		return texts[len(texts)-1]
	case <-time.After(2 * time.Second):
		t.Fatalf("no observation emitted; logs:\n%s", r.logged())
		return ""
	}
}

// DefaultSettings is the empty file shape: every value at its built-in default.
func DefaultSettings() Settings { return Settings{} }

func testConfig(endpoint string) Config {
	return Config{
		Enabled:      true,
		Endpoint:     endpoint,
		Model:        "exec",
		MaxEvents:    5,
		Debounce:     20 * time.Millisecond,
		Timeout:      2 * time.Second,
		MaxTokens:    50,
		SystemPrompt: "observe briefly",
	}
}

func TestSettingsDefaults(t *testing.T) {
	raw := []byte(`{"enabled":true,"endpoint":"http://127.0.0.1:4000/v1","model":"exec","debounceSeconds":2,"timeoutSeconds":5,"maxEvents":7}`)
	var settings Settings
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	cfg := settings.Config()
	if cfg.Debounce != 2*time.Second || cfg.Timeout != 5*time.Second || cfg.MaxEvents != 7 {
		t.Fatalf("explicit settings not applied: %+v", cfg)
	}
	if cfg.SystemPrompt != DefaultSystemPrompt || cfg.MaxTokens != DefaultMaxTokens {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
	if cfg.APIKeyEnv != "" {
		t.Fatalf("a key must never come from the file, got variable %q", cfg.APIKeyEnv)
	}

	zero := Settings{}.Config()
	if zero.MaxEvents != DefaultMaxEvents || zero.Debounce != DefaultDebounce ||
		zero.Timeout != DefaultTimeout || zero.MaxTokens != DefaultMaxTokens {
		t.Fatalf("zero settings did not take defaults: %+v", zero)
	}
	if err := zero.Validate(); err != nil {
		t.Fatalf("a disabled configuration must be valid: %v", err)
	}
}

func TestDefaultReportPromptRequiresBoundedFourSectionReport(t *testing.T) {
	for _, want := range []string{
		"exactly four sections", "what is in progress", "next steps explicitly stated by the agents",
		"blockers or decisions", "Always include all four sections", "at most 12 short bullets",
		"at most the five newest completed items", "Do not use fixed English headings",
	} {
		if !strings.Contains(DefaultSystemPrompt, want) {
			t.Errorf("DefaultSystemPrompt missing %q", want)
		}
	}
	if DefaultMaxTokens != 800 {
		t.Fatalf("DefaultMaxTokens = %d, want 800", DefaultMaxTokens)
	}
}

func TestAskPreservesTruncatedTextAndReportsDiagnostic(t *testing.T) {
	const body = `{"choices":[{"message":{"content":"partial four-section report"},"finish_reason":"length"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()
	o := &Observer{cfg: Config{Endpoint: srv.URL, Model: "exec", MaxTokens: 800}, key: "test", client: srv.Client()}
	got := o.ask(context.Background(), "", []events.Event{{Kind: events.KindAssistant, Text: "update"}})
	if got.text != "partial four-section report" {
		t.Fatalf("truncated text = %q", got.text)
	}
	if got.err == nil || !strings.Contains(got.err.Error(), "report truncated at maxTokens") {
		t.Fatalf("truncation diagnostic = %v", got.err)
	}
}

func TestObserverEmitsTruncatedTextAndSuppressesDuplicateReports(t *testing.T) {
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"**已完成**\n- parser"},"finish_reason":"length"}]}`)
	}))
	defer srv.Close()

	bus := events.NewBus()
	rec := newRecorder()
	cfg := testConfig(srv.URL + "/v1")
	obs, err := New(bus, cfg, rec.emit, rec.logf)
	if err != nil {
		t.Fatal(err)
	}
	obs.Start(context.Background())
	defer obs.Close()
	bus.Emit(events.Event{Kind: events.KindAssistant, Agent: protocol.Austin, Text: "parser implemented"})
	if got := waitEmit(t, rec); got != "**已完成**\n- parser" {
		t.Fatalf("truncated report = %q", got)
	}
	waitForObserver(t, func() bool { return strings.Contains(rec.logged(), "report truncated at maxTokens") })

	bus.Emit(events.Event{Kind: events.KindAssistant, Agent: protocol.Tony, Text: "tests passed"})
	waitForObserver(t, func() bool { return strings.Count(rec.logged(), "report truncated at maxTokens") == 2 })
	if got := rec.emittedText(); len(got) != 1 {
		t.Fatalf("emits = %#v, want identical second report suppressed", got)
	}
}

func waitForObserver(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("observer condition did not become true")
}

func TestValidateRejectsUnusableConfig(t *testing.T) {
	cases := map[string]Config{
		"no endpoint": {Enabled: true, Model: "exec"},
		"no model":    {Enabled: true, Endpoint: "http://127.0.0.1:4000/v1"},
		"bad scheme":  {Enabled: true, Endpoint: "ftp://127.0.0.1/v1", Model: "exec"},
		"bad url":     {Enabled: true, Endpoint: "://", Model: "exec"},
	}
	for name, cfg := range cases {
		cfg.MaxEvents, cfg.Debounce, cfg.Timeout = 1, time.Second, time.Second
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}

	enabled := testConfig("http://127.0.0.1:4000/v1")
	if err := enabled.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if _, err := New(events.NewBus(), enabled, func(string) {}, nil); err != nil {
		t.Fatalf("New rejected a valid config: %v", err)
	}
	disabled := enabled
	disabled.Enabled = false
	if _, err := New(events.NewBus(), disabled, func(string) {}, nil); err == nil {
		t.Fatal("New must not produce an observer for a disabled configuration")
	}
}

func TestObserverAsksModelAndEmitsObservation(t *testing.T) {
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")

	var (
		mu      sync.Mutex
		path    string
		auth    string
		body    string
		request int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		mu.Lock()
		path, auth, body = r.URL.Path, r.Header.Get("Authorization"), string(data)
		request++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"Austin claims success without running the test."}}]}`)
	}))
	defer srv.Close()

	bus := events.NewBus()
	rec := newRecorder()
	obs, err := New(bus, testConfig(srv.URL+"/v1"), rec.emit, rec.logf)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	obs.Start(context.Background())
	defer obs.Close()

	bus.Emit(events.Event{Kind: events.KindAssistant, Agent: protocol.Austin, Text: "implementation done"})
	text := waitEmit(t, rec)
	if text != "Austin claims success without running the test." {
		t.Fatalf("observation = %q", text)
	}

	mu.Lock()
	defer mu.Unlock()
	if request != 1 {
		t.Fatalf("requests = %d, want 1", request)
	}
	if path != "/v1/chat/completions" {
		t.Fatalf("path = %q, want /v1/chat/completions (endpoint is the base URL)", path)
	}
	if auth != "Bearer sk-test" {
		t.Fatalf("authorization = %q", auth)
	}
	var sent struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		Messages  []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatalf("decode request: %v\n%s", err, body)
	}
	if sent.Model != "exec" || sent.MaxTokens != 50 {
		t.Fatalf("request model/max_tokens = %q/%d", sent.Model, sent.MaxTokens)
	}
	if len(sent.Messages) != 2 || sent.Messages[0].Role != "system" || sent.Messages[1].Role != "user" {
		t.Fatalf("unexpected message shape: %+v", sent.Messages)
	}
	if !strings.Contains(sent.Messages[1].Content, "Austin [assistant]: implementation done") {
		t.Fatalf("transcript does not identify the speaker and kind: %q", sent.Messages[1].Content)
	}
}

func TestObserverCarriesPreviousReportIntoIncrementalUpdate(t *testing.T) {
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")
	var mu sync.Mutex
	var prompts []string
	var request int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		mu.Lock()
		prompts = append(prompts, payload.Messages[1].Content)
		request++
		n := request
		mu.Unlock()
		report := "Report one: parser implemented."
		if n > 1 {
			report = "Report two: parser implemented; tests now pass."
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"`+report+`"}}]}`)
	}))
	defer srv.Close()

	bus := events.NewBus()
	rec := newRecorder()
	cfg := testConfig(srv.URL + "/v1")
	cfg.Debounce = 20 * time.Millisecond
	obs, err := New(bus, cfg, rec.emit, rec.logf)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	obs.Start(context.Background())
	defer obs.Close()
	bus.Emit(events.Event{Kind: events.KindAssistant, Agent: protocol.Austin, Text: "parser implemented"})
	if got := waitEmit(t, rec); got != "Report one: parser implemented." {
		t.Fatalf("first report = %q", got)
	}
	bus.Emit(events.Event{Kind: events.KindAssistant, Agent: protocol.Tony, Text: "tests now pass"})
	if got := waitEmit(t, rec); got != "Report two: parser implemented; tests now pass." {
		t.Fatalf("second report = %q", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(prompts) != 2 || !strings.Contains(prompts[1], "Previous running report:\nReport one: parser implemented.") || !strings.Contains(prompts[1], "Tony [assistant]: tests now pass") {
		t.Fatalf("second incremental prompt = %#v", prompts)
	}
}

func TestObserverCoalescesOneRequestPerBurst(t *testing.T) {
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")

	var (
		mu       sync.Mutex
		requests []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, string(data))
		n := len(requests)
		mu.Unlock()
		content := "noted first"
		if n > 1 {
			content = "noted second"
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"`+content+`"}}]}`)
	}))
	defer srv.Close()

	bus := events.NewBus()
	rec := newRecorder()
	obs, err := New(bus, testConfig(srv.URL+"/v1"), rec.emit, rec.logf)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	obs.Start(context.Background())
	defer obs.Close()

	for _, text := range []string{"first", "second", "third"} {
		bus.Emit(events.Event{Kind: events.KindPeer, Agent: protocol.Austin, Peer: protocol.Tony, Text: text})
		time.Sleep(2 * time.Millisecond)
	}
	waitEmit(t, rec)

	bus.Emit(events.Event{Kind: events.KindUser, Agent: protocol.Duo, Text: "fourth"})
	waitEmit(t, rec)

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2 (one per burst): %v", len(requests), requests)
	}
	for _, text := range []string{"first", "second", "third"} {
		if !strings.Contains(requests[0], text) {
			t.Fatalf("first request lost %q:\n%s", text, requests[0])
		}
	}
	if strings.Contains(requests[0], "fourth") {
		t.Fatal("second burst leaked into the first request")
	}
	if !strings.Contains(requests[1], "fourth") {
		t.Fatalf("second request missing the new event:\n%s", requests[1])
	}
}

func TestObserverKeepsSilentReplyOutOfTimeline(t *testing.T) {
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":"NOTHING"}}]}`)
	}))
	defer srv.Close()

	bus := events.NewBus()
	rec := newRecorder()
	obs, err := New(bus, testConfig(srv.URL+"/v1"), rec.emit, rec.logf)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	obs.Start(context.Background())
	defer obs.Close()

	bus.Emit(events.Event{Kind: events.KindAssistant, Agent: protocol.Tony, Text: "ok"})
	time.Sleep(150 * time.Millisecond)
	if got := rec.emittedText(); len(got) != 0 {
		t.Fatalf("NOTHING must not be shown, got %q", got)
	}
}

func TestObserverWithoutKeySendsNothing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DUO_OBSERVER_API_KEY", "")
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		io.WriteString(w, `{"choices":[{"message":{"content":"should not happen"}}]}`)
	}))
	defer srv.Close()

	bus := events.NewBus()
	rec := newRecorder()
	obs, err := New(bus, testConfig(srv.URL+"/v1"), rec.emit, rec.logf)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	obs.Start(context.Background())
	defer obs.Close()

	bus.Emit(events.Event{Kind: events.KindAssistant, Agent: protocol.Austin, Text: "no key here"})
	time.Sleep(150 * time.Millisecond)

	if got := requests.Load(); got != 0 {
		t.Fatalf("requests = %d without a key, want 0", got)
	}
	if got := rec.emittedText(); len(got) != 0 {
		t.Fatalf("unexpected observation without a key: %q", got)
	}
	if !strings.Contains(rec.logged(), "no API key") {
		t.Fatalf("missing one-line diagnostic, logs:\n%s", rec.logged())
	}
}

func TestAPIKeyPrecedence(t *testing.T) {
	// Keep a real ~/.duo/observer.key on the operator's machine out of this test.
	t.Setenv("HOME", t.TempDir())
	cfg := Config{APIKeyEnv: "OBSERVER_TEST_KEY"}
	t.Setenv("OBSERVER_TEST_KEY", "from-name")
	if got := cfg.APIKey(); got != "from-name" {
		t.Fatalf("APIKey from name = %q", got)
	}
	t.Setenv("DUO_OBSERVER_API_KEY", "from-raw")
	if got := cfg.APIKey(); got != "from-raw" {
		t.Fatalf("raw environment key must win, got %q", got)
	}
	t.Setenv("DUO_OBSERVER_API_KEY", "")
	cfg.APIKeyEnv = ""
	if got := cfg.APIKey(); got != "" {
		t.Fatalf("APIKey = %q, want empty", got)
	}
}

func TestObserverIsolatesModelFailures(t *testing.T) {
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")

	var fail atomic.Bool
	fail.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "upstream exploded", http.StatusInternalServerError)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"recovered"}}]}`)
	}))
	defer srv.Close()

	bus := events.NewBus()
	rec := newRecorder()
	obs, err := New(bus, testConfig(srv.URL+"/v1"), rec.emit, rec.logf)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	obs.Start(context.Background())
	defer obs.Close()

	bus.Emit(events.Event{Kind: events.KindAssistant, Agent: protocol.Austin, Text: "doomed request"})
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(rec.logged(), "500") {
		if time.Now().After(deadline) {
			t.Fatalf("failure was not logged: %q", rec.logged())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := rec.emittedText(); len(got) != 0 {
		t.Fatalf("a failed request must not emit: %q", got)
	}

	fail.Store(false)
	bus.Emit(events.Event{Kind: events.KindAssistant, Agent: protocol.Tony, Text: "after the failure"})
	if got := waitEmit(t, rec); got != "recovered" {
		t.Fatalf("observer did not recover after a failure: %q", got)
	}
}

func TestObserverTimesOutWithoutBlockingTheBus(t *testing.T) {
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		io.WriteString(w, `{"choices":[{"message":{"content":"too late"}}]}`)
	}))
	defer srv.Close()
	defer close(release)

	bus := events.NewBus()
	rec := newRecorder()
	cfg := testConfig(srv.URL + "/v1")
	cfg.Timeout = 40 * time.Millisecond
	obs, err := New(bus, cfg, rec.emit, rec.logf)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	obs.Start(context.Background())
	defer obs.Close()

	// A full bus subscriber buffer must never make Emit block, so a thousand
	// events during a stalled request are dropped rather than queued forever.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			bus.Emit(events.Event{Kind: events.KindAssistant, Agent: protocol.Austin, Text: "burst"})
		}
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Emit blocked while the observer's request was stalled")
	}

	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(rec.logged(), "deadline exceeded") && !strings.Contains(rec.logged(), "timeout") {
		if time.Now().After(deadline) {
			t.Fatalf("timeout was not logged: %q", rec.logged())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := rec.emittedText(); len(got) != 0 {
		t.Fatalf("a timed-out request must not emit: %q", got)
	}
}

func TestObserverCloseIsIdempotent(t *testing.T) {
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")

	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		io.WriteString(w, `{"choices":[{"message":{"content":"late"}}]}`)
	}))
	defer srv.Close()

	bus := events.NewBus()
	rec := newRecorder()
	obs, err := New(bus, testConfig(srv.URL+"/v1"), rec.emit, rec.logf)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Closing an observer that never started is a no-op.
	obs.Close()

	obs.Start(context.Background())
	obs.Close()
	obs.Close()

	// Starting a closed observer must be a no-op rather than a second close of
	// its completion channel.
	obs.Start(context.Background())
	obs.Close()

	bus.Emit(events.Event{Kind: events.KindAssistant, Agent: protocol.Austin, Text: "after close"})
	time.Sleep(150 * time.Millisecond)
	if got := requests.Load(); got != 0 {
		t.Fatalf("requests after Close = %d, want 0", got)
	}
	if got := rec.emittedText(); len(got) != 0 {
		t.Fatalf("observation after Close: %q", got)
	}
}

func TestObserverIgnoresItsOwnOutput(t *testing.T) {
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")

	var (
		mu     sync.Mutex
		bodies []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(data))
		mu.Unlock()
		io.WriteString(w, `{"choices":[{"message":{"content":"observed"}}]}`)
	}))
	defer srv.Close()

	bus := events.NewBus()
	rec := newRecorder()
	obs, err := New(bus, testConfig(srv.URL+"/v1"), rec.emit, rec.logf)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	obs.Start(context.Background())
	defer obs.Close()

	// An observation is published on the same bus the observer reads. Feeding it
	// back would make the observer answer itself forever.
	bus.Emit(events.Event{Kind: events.KindObserver, Agent: protocol.Duo, Text: "an earlier observation"})
	time.Sleep(120 * time.Millisecond)
	mu.Lock()
	got := len(bodies)
	mu.Unlock()
	if got != 0 {
		t.Fatalf("the observer asked the model about its own output (%d requests)", got)
	}
	if got := rec.emittedText(); len(got) != 0 {
		t.Fatalf("the observer re-emitted its own output: %q", got)
	}

	bus.Emit(events.Event{Kind: events.KindAssistant, Agent: protocol.Austin, Text: "real work"})
	if got := waitEmit(t, rec); got != "observed" {
		t.Fatalf("observation = %q", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("requests = %d, want 1", len(bodies))
	}
	if strings.Contains(bodies[0], "an earlier observation") {
		t.Fatalf("observer output reached the prompt:\n%s", bodies[0])
	}
}

// A save from the four-field interface must persist the tuning that is in force
// without materialising defaults, so a restart runs what the session ran and a
// config file does not grow a copy of the built-in prompt.
func TestFileSettingsKeepsCustomValuesAndDropsDefaults(t *testing.T) {
	stock := DefaultSettings()
	if got := stock.Config().FileSettings(); got != (Settings{}) {
		t.Fatalf("stock configuration persisted values it did not choose: %+v", got)
	}

	custom := Config{
		Enabled: true, Endpoint: "http://127.0.0.1:4000/v1", Model: "exec",
		APIKeyEnv: "MY_KEY", SystemPrompt: "be terse", MaxEvents: 9,
		Debounce: 2 * time.Second, Timeout: 30 * time.Second, MaxTokens: 120,
	}
	settings := custom.FileSettings()
	if settings.SystemPrompt != "be terse" || settings.MaxEvents != 9 ||
		settings.DebounceSeconds != 2 || settings.TimeoutSeconds != 30 || settings.MaxTokens != 120 {
		t.Fatalf("custom settings lost: %+v", settings)
	}
	roundTrip := settings.Config()
	if roundTrip.SystemPrompt != "be terse" || roundTrip.MaxEvents != 9 || roundTrip.Debounce != 2*time.Second ||
		roundTrip.Timeout != 30*time.Second || roundTrip.MaxTokens != 120 {
		t.Fatalf("round trip changed the configuration: %+v", roundTrip)
	}
}
