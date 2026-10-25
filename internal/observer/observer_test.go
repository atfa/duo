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

// logRecorder collects the diagnostics an observer reports.
type logRecorder struct {
	mu   sync.Mutex
	logs []string
}

func (r *logRecorder) logf(format string, args ...any) {
	r.mu.Lock()
	r.logs = append(r.logs, fmt.Sprintf(format, args...))
	r.mu.Unlock()
}

func (r *logRecorder) logged() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.logs, "\n")
}

// DefaultSettings is the empty file shape: every value at its built-in default.
func DefaultSettings() Settings { return Settings{} }

func testConfig(endpoint string) Config {
	return Config{
		Enabled:      true,
		Endpoint:     endpoint,
		Model:        "exec",
		MaxEvents:    5,
		Timeout:      2 * time.Second,
		MaxTokens:    50,
		SystemPrompt: "observe briefly",
	}
}

// newTestObserver returns an observer wired to a bus and a fixed live block.
func newTestObserver(t *testing.T, cfg Config, live string) (*Observer, *logRecorder) {
	t.Helper()
	rec := &logRecorder{}
	obs, err := New(events.NewBus(), cfg, func() string { return live }, rec.logf)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return obs, rec
}

// waitWindow blocks until the collector holds at least want events, so a test
// never races the bus subscription.
func waitWindow(t *testing.T, o *Observer, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		o.mu.Lock()
		n := len(o.window)
		o.mu.Unlock()
		if n >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("recent log never held %d events", want)
}

// waitNewest blocks until the collector's newest event contains want.
func waitNewest(t *testing.T, o *Observer, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		o.mu.Lock()
		var newest string
		if n := len(o.window); n > 0 {
			newest = o.window[n-1].Text
		}
		o.mu.Unlock()
		if strings.Contains(newest, want) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("the recent log never reached %q", want)
}

func TestSettingsDefaults(t *testing.T) {
	// debounceSeconds is no longer an observer setting. An old file that still
	// carries it must load without failing rather than break a working setup.
	raw := []byte(`{"enabled":true,"endpoint":"http://127.0.0.1:4000/v1","model":"exec","debounceSeconds":2,"timeoutSeconds":5,"maxEvents":7}`)
	var settings Settings
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	cfg := settings.Config()
	if !cfg.Enabled || cfg.Endpoint != "http://127.0.0.1:4000/v1" || cfg.Model != "exec" {
		t.Fatalf("explicit values lost: %+v", cfg)
	}
	if cfg.MaxEvents != 7 || cfg.Timeout != 5*time.Second {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
	if cfg.APIKeyEnv != "" {
		t.Fatalf("a key must never come from the file, got variable %q", cfg.APIKeyEnv)
	}

	zero := Settings{}.Config()
	if zero.MaxEvents != DefaultMaxEvents || zero.Timeout != DefaultTimeout || zero.MaxTokens != DefaultMaxTokens {
		t.Fatalf("zero settings did not take defaults: %+v", zero)
	}
	if err := zero.Validate(); err != nil {
		t.Fatalf("a disabled configuration must be valid: %v", err)
	}
}

func TestDefaultReportPromptRequiresBoundedFiveSectionReport(t *testing.T) {
	for _, want := range []string{
		"exactly five sections", "Progress", "Just finished", "Doing now", "Watch out",
		"derived ONLY from the Current state block", "Never describe a finished tool as running",
		"only what the agents explicitly stated they will do", "Needs your decision:",
		"at most twenty short lines", "including the section headings",
		"Task", "comes ONLY from the Current state block",
		// Watch out is optional by design: it is omitted rather than filled.
		"omit this " + "section and its heading entirely",
		// A question turns the same blocks into a direct answer.
		"Question from the human", "at most eight short lines",
	} {
		if !strings.Contains(DefaultSystemPrompt, want) {
			t.Errorf("DefaultSystemPrompt missing %q", want)
		}
	}
	if DefaultMaxTokens != 1000 {
		t.Fatalf("DefaultMaxTokens = %d, want 1000", DefaultMaxTokens)
	}
}

// A question is the human asking the observer to look at one thing, so the
// prompt carries the question last and the answer keeps the previous report as
// continuity: asking follow-up questions must not cost the observer its context.
func TestAnswerCarriesQuestionAndSharesReportContinuity(t *testing.T) {
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")

	var (
		mu      sync.Mutex
		prompts []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		mu.Lock()
		prompts = append(prompts, payload.Messages[1].Content)
		mu.Unlock()
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"answer"}}]}`)
	}))
	defer srv.Close()

	obs, _ := newTestObserver(t, testConfig(srv.URL+"/v1"), "Austin: idle")

	if got, err := obs.Report(context.Background()); err != nil || got != "answer" {
		t.Fatalf("report = %q, %v", got, err)
	}
	question := "why is Tony stuck?\n  he has been on the same tool"
	if got, err := obs.Answer(context.Background(), question); err != nil || got != "answer" {
		t.Fatalf("answer = %q, %v", got, err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(prompts) != 2 {
		t.Fatalf("prompts = %d, want 2", len(prompts))
	}
	if strings.Contains(prompts[0], "Question from the human") {
		t.Fatalf("a plain report carried a question block:\n%s", prompts[0])
	}
	second := prompts[1]
	// The question stays one line, so a wrapped panel and the prompt agree.
	if want := "Question from the human (answer this instead of writing the report):\nwhy is Tony stuck? he has been on the same tool\n"; !strings.Contains(second, want) {
		t.Fatalf("answer prompt is missing the normalized question %q:\n%s", want, second)
	}
	if !strings.Contains(second, "Previous report (continuity only; do not repeat its finished bullets):\nanswer") {
		t.Fatalf("the answer dropped the previous report:\n%s", second)
	}
	if strings.LastIndex(second, "Question from the human") < strings.LastIndex(second, "Previous report") {
		t.Fatalf("the question is not the last block:\n%s", second)
	}
}

// An empty question is not a request: nothing is sent and the caller is told why.
func TestAnswerRejectsAnEmptyQuestion(t *testing.T) {
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")

	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"answer"}}]}`)
	}))
	defer srv.Close()

	obs, _ := newTestObserver(t, testConfig(srv.URL+"/v1"), "Austin: idle")
	if _, err := obs.Answer(context.Background(), "  \n\t "); err == nil {
		t.Fatal("an empty question was accepted")
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("an empty question sent %d request(s)", got)
	}
}

func TestNormalizeQuestionKeepsOneBoundedLine(t *testing.T) {
	if got, want := NormalizeQuestion("  what\n is\tthe  plan?  "), "what is the plan?"; got != want {
		t.Fatalf("NormalizeQuestion = %q, want %q", got, want)
	}
	long := NormalizeQuestion(strings.Repeat("x", maxQuestionRunes+50))
	if got := len([]rune(long)); got != maxQuestionRunes+1 {
		t.Fatalf("bounded question is %d runes, want %d plus the cut marker", got, maxQuestionRunes)
	}
	if !strings.HasSuffix(long, "…") {
		t.Fatalf("a cut question lost its marker: %q", long[max(0, len(long)-10):])
	}
}

func TestReportAnchorsPromptOnLatestHumanTask(t *testing.T) {
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")

	var prompt string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		prompt = payload.Messages[1].Content
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"report"}}]}`)
	}))
	defer srv.Close()

	obs, _ := newTestObserver(t, testConfig(srv.URL+"/v1"), "Austin: idle")
	obs.Start(context.Background())
	defer obs.Close()
	obs.bus.Emit(events.Event{Kind: events.KindUser, Text: "optimise the observer prompt"})
	waitNewest(t, obs, "optimise the observer prompt")

	if _, err := obs.Report(context.Background()); err != nil {
		t.Fatalf("Report: %v", err)
	}
	if !strings.Contains(prompt, "Task (the human's original request for this round):\noptimise the observer prompt") {
		t.Fatalf("prompt does not anchor on the human task:\n%s", prompt)
	}

	// A later human message replaces the anchor, and a task that scrolls out of
	// the event window is still remembered.
	for i := 0; i < 50; i++ {
		obs.bus.Emit(events.Event{Kind: events.KindAssistant, Agent: protocol.Austin, Text: fmt.Sprintf("filler %d", i)})
	}
	waitNewest(t, obs, "filler 49")
	obs.bus.Emit(events.Event{Kind: events.KindUser, Text: "follow-up task"})
	waitNewest(t, obs, "follow-up task")
	if _, err := obs.Report(context.Background()); err != nil {
		t.Fatalf("second Report: %v", err)
	}
	if !strings.Contains(prompt, "follow-up task") || strings.Contains(prompt, "optimise the observer prompt") {
		t.Fatalf("stale task anchor:\n%s", prompt)
	}
}

func TestReportSendsLiveContextAndRecentLog(t *testing.T) {
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")

	var (
		mu   sync.Mutex
		path string
		auth string
		body string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		mu.Lock()
		path, auth, body = r.URL.Path, r.Header.Get("Authorization"), string(data)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"**Doing now**\n- Austin is running go test"}}]}`)
	}))
	defer srv.Close()

	obs, _ := newTestObserver(t, testConfig(srv.URL+"/v1"), "Austin: busy running tool bash(go test ./...) for 12s")
	obs.Start(context.Background())
	defer obs.Close()
	obs.bus.Emit(events.Event{Kind: events.KindAssistant, Agent: protocol.Austin, Text: "implementation done"})
	obs.bus.Emit(events.Event{Kind: events.KindActivity, Agent: protocol.Austin})
	waitNewest(t, obs, "implementation done")

	text, err := obs.Report(context.Background())
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if text != "**Doing now**\n- Austin is running go test" {
		t.Fatalf("report = %q", text)
	}

	mu.Lock()
	defer mu.Unlock()
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
	prompt := sent.Messages[1].Content
	if !strings.Contains(prompt, "Current state") || !strings.Contains(prompt, "running tool bash(go test ./...)") {
		t.Fatalf("prompt does not carry the live block:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Austin [assistant]: implementation done") {
		t.Fatalf("prompt does not identify the speaker and kind:\n%s", prompt)
	}
	if strings.Contains(prompt, "[activity]") {
		t.Fatalf("an empty activity event reached the log:\n%s", prompt)
	}
}

func TestReportSamplesLiveContextAtRequestTime(t *testing.T) {
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")

	var (
		mu      sync.Mutex
		prompts []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		mu.Lock()
		prompts = append(prompts, payload.Messages[1].Content)
		mu.Unlock()
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"report"}}]}`)
	}))
	defer srv.Close()

	var samples atomic.Int32
	rec := &logRecorder{}
	obs, err := New(events.NewBus(), testConfig(srv.URL+"/v1"), func() string {
		return fmt.Sprintf("Austin: busy running tool bash(step %d)", samples.Add(1))
	}, rec.logf)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := obs.Report(context.Background()); err != nil {
			t.Fatalf("Report %d: %v", i, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(prompts) != 2 {
		t.Fatalf("prompts = %d, want 2", len(prompts))
	}
	if !strings.Contains(prompts[0], "step 1") || !strings.Contains(prompts[1], "step 2") {
		t.Fatalf("the live block was not sampled per request: %#v", prompts)
	}
}

func TestReportCarriesPreviousReportAsContinuityOnly(t *testing.T) {
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")

	var (
		mu      sync.Mutex
		prompts []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		mu.Lock()
		prompts = append(prompts, payload.Messages[1].Content)
		n := len(prompts)
		mu.Unlock()
		report := "Report one: parser implemented."
		if n > 1 {
			report = "Report two: tests now pass."
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"`+report+`"}}]}`)
	}))
	defer srv.Close()

	obs, _ := newTestObserver(t, testConfig(srv.URL+"/v1"), "Austin: idle")
	obs.Start(context.Background())
	defer obs.Close()

	obs.bus.Emit(events.Event{Kind: events.KindAssistant, Agent: protocol.Austin, Text: "parser implemented"})
	waitNewest(t, obs, "parser implemented")
	if got, err := obs.Report(context.Background()); err != nil || got != "Report one: parser implemented." {
		t.Fatalf("first report = %q, %v", got, err)
	}
	obs.bus.Emit(events.Event{Kind: events.KindAssistant, Agent: protocol.Tony, Text: "tests now pass"})
	waitNewest(t, obs, "tests now pass")
	if got, err := obs.Report(context.Background()); err != nil || got != "Report two: tests now pass." {
		t.Fatalf("second report = %q, %v", got, err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(prompts) != 2 {
		t.Fatalf("prompts = %d, want 2", len(prompts))
	}
	if strings.Contains(prompts[0], "Previous report") {
		t.Fatalf("the first request must not claim a previous report:\n%s", prompts[0])
	}
	second := prompts[1]
	if !strings.Contains(second, "Previous report (continuity only; do not repeat its finished bullets):\nReport one: parser implemented.") {
		t.Fatalf("second prompt lost the previous report:\n%s", second)
	}
	if !strings.Contains(second, "Tony [assistant]: tests now pass") {
		t.Fatalf("second prompt lost the newest event:\n%s", second)
	}
}

func TestReportKeepsTheRecentLogBounded(t *testing.T) {
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")

	var (
		mu     sync.Mutex
		prompt string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	defer srv.Close()

	cfg := testConfig(srv.URL + "/v1")
	cfg.MaxEvents = 3
	obs, _ := newTestObserver(t, cfg, "Austin: idle")
	obs.Start(context.Background())
	defer obs.Close()

	// A long log must lose its oldest events to both the count and the rune
	// budget, so one request cannot grow without limit.
	bulk := strings.Repeat("x", 3000)
	for i := 0; i < 6; i++ {
		obs.bus.Emit(events.Event{Kind: events.KindAssistant, Agent: protocol.Austin, Text: fmt.Sprintf("event-%d %s", i, bulk)})
	}
	waitNewest(t, obs, "event-5 ")
	if _, err := obs.Report(context.Background()); err != nil {
		t.Fatalf("Report: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if strings.Contains(prompt, "event-0 ") || strings.Contains(prompt, "event-2 ") {
		t.Fatalf("the window kept events beyond its budget:\n%.400s", prompt)
	}
	if !strings.Contains(prompt, "event-5 ") {
		t.Fatalf("the window dropped the newest event:\n%.400s", prompt)
	}
	if runes := len([]rune(prompt)); runes > maxTranscriptRunes+4000 {
		t.Fatalf("prompt grew to %d runes, beyond the transcript budget", runes)
	}
}

func TestReportPreservesTruncatedTextAndReportsDiagnostic(t *testing.T) {
	const body = `{"choices":[{"message":{"content":"partial three-section report"},"finish_reason":"length"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()
	obs := &Observer{cfg: Config{Endpoint: srv.URL, Model: "exec", MaxTokens: 800}, key: "test", client: srv.Client(), live: func() string { return "" }}
	got, err := obs.Report(context.Background())
	if got != "partial three-section report" {
		t.Fatalf("truncated text = %q", got)
	}
	if err == nil || !strings.Contains(err.Error(), "report truncated at maxTokens") {
		t.Fatalf("truncation diagnostic = %v", err)
	}
}

func TestReportRejectsAnEmptyAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"   "}}]}`)
	}))
	defer srv.Close()
	obs := &Observer{cfg: Config{Endpoint: srv.URL, Model: "exec"}, key: "test", client: srv.Client(), live: func() string { return "" }}
	if _, err := obs.Report(context.Background()); err == nil || !strings.Contains(err.Error(), "empty report") {
		t.Fatalf("empty answer error = %v", err)
	}
}

func TestValidateRejectsUnusableConfig(t *testing.T) {
	cases := map[string]Config{
		"no endpoint": {Enabled: true, Model: "exec"},
		"no model":    {Enabled: true, Endpoint: "http://127.0.0.1:4000/v1"},
		"bad scheme":  {Enabled: true, Endpoint: "ftp://127.0.0.1/v1", Model: "exec"},
		"bad url":     {Enabled: true, Endpoint: "://", Model: "exec"},
	}
	for name, cfg := range cases {
		cfg.MaxEvents, cfg.Timeout = 1, time.Second
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}

	enabled := testConfig("http://127.0.0.1:4000/v1")
	if err := enabled.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if _, err := New(events.NewBus(), enabled, func() string { return "" }, nil); err != nil {
		t.Fatalf("New rejected a valid config: %v", err)
	}
	disabled := enabled
	disabled.Enabled = false
	if _, err := New(events.NewBus(), disabled, func() string { return "" }, nil); err == nil {
		t.Fatal("New must not produce an observer for a disabled configuration")
	}
}

func TestReportWithoutKeySendsNothing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DUO_OBSERVER_API_KEY", "")
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"should not happen"}}]}`)
	}))
	defer srv.Close()

	obs, rec := newTestObserver(t, testConfig(srv.URL+"/v1"), "")
	if _, err := obs.Report(context.Background()); err == nil || !strings.Contains(err.Error(), "no API key") {
		t.Fatalf("missing-key error = %v", err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("requests = %d without a key, want 0", got)
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

func TestReportIsolatesModelFailures(t *testing.T) {
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")

	var fail atomic.Bool
	fail.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "upstream exploded", http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"recovered"}}]}`)
	}))
	defer srv.Close()

	obs, _ := newTestObserver(t, testConfig(srv.URL+"/v1"), "Austin: idle")
	if _, err := obs.Report(context.Background()); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("failure error = %v", err)
	}
	fail.Store(false)
	if got, err := obs.Report(context.Background()); err != nil || got != "recovered" {
		t.Fatalf("observer did not recover after a failure: %q, %v", got, err)
	}
}

func TestReportTimesOutInsteadOfHanging(t *testing.T) {
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"too late"}}]}`)
	}))
	defer srv.Close()
	defer close(release)

	cfg := testConfig(srv.URL + "/v1")
	cfg.Timeout = 40 * time.Millisecond
	obs, _ := newTestObserver(t, cfg, "Austin: idle")
	start := time.Now()
	text, err := obs.Report(context.Background())
	if err == nil {
		t.Fatalf("a stalled request must fail, got report %q", text)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("a timed-out request took %s", elapsed)
	}
}

func TestObserverCloseIsIdempotent(t *testing.T) {
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")

	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"late"}}]}`)
	}))
	defer srv.Close()

	obs, _ := newTestObserver(t, testConfig(srv.URL+"/v1"), "")
	// Closing an observer that never started is a no-op.
	obs.Close()

	obs.Start(context.Background())
	obs.Close()
	obs.Close()

	// Starting a closed observer must be a no-op rather than a second close of
	// its completion channel.
	obs.Start(context.Background())
	obs.Close()

	if got := requests.Load(); got != 0 {
		t.Fatalf("requests after Close = %d, want 0", got)
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
		Timeout: 30 * time.Second, MaxTokens: 120,
	}
	settings := custom.FileSettings()
	if settings.SystemPrompt != "be terse" || settings.MaxEvents != 9 ||
		settings.TimeoutSeconds != 30 || settings.MaxTokens != 120 {
		t.Fatalf("custom settings lost: %+v", settings)
	}
	roundTrip := settings.Config()
	if roundTrip.SystemPrompt != "be terse" || roundTrip.MaxEvents != 9 ||
		roundTrip.Timeout != 30*time.Second || roundTrip.MaxTokens != 120 {
		t.Fatalf("round trip changed the configuration: %+v", roundTrip)
	}
}
