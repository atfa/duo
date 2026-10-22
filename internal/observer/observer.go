// Package observer is Duo's optional advisory observer.
//
// It watches the session's own event stream and, when a human asks for one
// status report, asks an OpenAI-compatible model for it. It is deliberately
// passive: it reads the existing event bus, whose Emit drops rather than blocks,
// it runs no timer, and it never writes workflow state, talks to an agent or owns
// a phase.
//
// A report answers three human questions: what just finished, what is happening
// right now, and what comes next. "Right now" cannot come from the event stream,
// because a running tool is not a message, so the caller supplies a live context
// provider: a block sampled from Duo's runtime at request time.
//
// An observer that is disabled, misconfigured, slow or broken must be
// indistinguishable from no observer at all, so every failure here is an error
// the caller turns into a log line and a notice, nothing else.
package observer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/atfa/duo/internal/events"
)

const (
	// DefaultMaxEvents bounds how many recent events one report reads.
	DefaultMaxEvents = 40
	// Default per-request budget.
	DefaultTimeout = 60 * time.Second
	// Default answer length; a status report has three required sections.
	DefaultMaxTokens = 800

	// busBuffer is large enough for a burst of tool activity. The bus drops when
	// a subscriber falls behind, which is the behaviour this package relies on.
	busBuffer = 256

	// maxEventRunes bounds one event's contribution to a prompt,
	// maxTranscriptRunes bounds the whole recent log so a chatty session cannot
	// price a human's Ctrl+E out of reach, and maxErrorBody bounds how much of a
	// failed response is logged.
	maxEventRunes      = 1200
	maxTranscriptRunes = 6000
	maxErrorBody       = 512
)

// DefaultSystemPrompt asks for a status report about the agents, not for advice.
//
// The three sections are the human's own questions. The "Doing now" rule is the
// part that makes the report worth opening: it may only use the live block, so a
// finished tool can never be presented as work in progress, which is exactly what
// an event-only observer used to get wrong.
const DefaultSystemPrompt = "You are Duo's observer. Duo is a coding workflow where two agents, Austin and Tony, " +
	"work on one task in separate Git worktrees. You write a short status report for the human watching them, in " +
	"the human's language, including the section headings. Each request carries three blocks: 'Current state' " +
	"(sampled live right now from Duo's runtime: mode, phase, step, and per agent whether it is busy or idle, the " +
	"tool it is running with its arguments and elapsed time, how long the current turn has lasted, the last few " +
	"finished tools, the text it is currently writing, and its context usage); 'Recent session log' (the newest " +
	"messages between the agents, the human and Duo); and 'Previous report' (your last report, for continuity " +
	"only).\n\n" +
	"Write exactly three sections:\n" +
	"1. Just finished - at most three bullets, newest first, each a concrete result. Never repeat a bullet from " +
	"the previous report, never accumulate history, and drop anything newer events have superseded.\n" +
	"2. Doing now - one line per agent, derived ONLY from the Current state block, never guessed from the log. " +
	"Name the agent, say whether it is busy or idle, and say what it is doing: the running tool with its " +
	"arguments and how long it has run, the file it is editing, or the first words of the text it is currently " +
	"writing. An idle agent is reported as idle together with what it finished last. Never describe a finished " +
	"tool as running.\n" +
	"3. Next - only what the agents explicitly stated they will do, in their order, at most three bullets. When " +
	"they stated nothing, say so in one short line.\n\n" +
	"End with one final line starting with the human's words for 'Needs your decision:' only when a decision " +
	"genuinely blocks the agents; otherwise omit that line entirely. Do not invent facts, do not advise the " +
	"agents, do not repeat a bullet twice, and keep the whole report to at most twelve short lines."

// Config is the resolved observer configuration. It is what the session runs
// with; Settings is the file-facing shape.
type Config struct {
	Enabled      bool
	Endpoint     string
	Model        string
	APIKeyEnv    string
	SystemPrompt string
	MaxEvents    int
	Timeout      time.Duration
	MaxTokens    int
}

// Settings is the JSON shape of the observer section of a Duo config file.
//
// Durations are seconds because the file is hand-written. A key is never part of
// the file: APIKeyEnv names the environment variable that holds it, and the
// process environment's DUO_OBSERVER_API_KEY wins over that name.
type Settings struct {
	Enabled        bool   `json:"enabled,omitempty"`
	Endpoint       string `json:"endpoint,omitempty"`
	Model          string `json:"model,omitempty"`
	APIKeyEnv      string `json:"apiKeyEnv,omitempty"`
	SystemPrompt   string `json:"systemPrompt,omitempty"`
	MaxEvents      int    `json:"maxEvents,omitempty"`
	TimeoutSeconds int    `json:"timeoutSeconds,omitempty"`
	MaxTokens      int    `json:"maxTokens,omitempty"`
}

// Config applies defaults to the file settings. It does not validate them.
func (s Settings) Config() Config {
	return Config{
		Enabled:      s.Enabled,
		Endpoint:     s.Endpoint,
		Model:        s.Model,
		APIKeyEnv:    s.APIKeyEnv,
		SystemPrompt: s.SystemPrompt,
		MaxEvents:    s.MaxEvents,
		Timeout:      time.Duration(s.TimeoutSeconds) * time.Second,
		MaxTokens:    s.MaxTokens,
	}.Normalized()
}

// Normalized fills in every unspecified tuning field with its default.
//
// It deliberately leaves Enabled, Endpoint, Model and APIKeyEnv alone: those are
// the operator's choices, and silently replacing an empty endpoint with a default
// would hide a misconfiguration. A configuration that arrives from anywhere other
// than a file (a live switch from the interface, for example) is normalized first
// so the observer never runs with a missing prompt.
func (c Config) Normalized() Config {
	c.Endpoint = strings.TrimSpace(c.Endpoint)
	c.Model = strings.TrimSpace(c.Model)
	c.APIKeyEnv = strings.TrimSpace(c.APIKeyEnv)
	c.SystemPrompt = strings.TrimSpace(c.SystemPrompt)
	if c.SystemPrompt == "" {
		c.SystemPrompt = DefaultSystemPrompt
	}
	if c.MaxEvents <= 0 {
		c.MaxEvents = DefaultMaxEvents
	}
	if c.Timeout <= 0 {
		c.Timeout = DefaultTimeout
	}
	if c.MaxTokens <= 0 {
		c.MaxTokens = DefaultMaxTokens
	}
	return c
}

// Validate reports why an enabled configuration cannot run. A disabled
// configuration is always valid, because it does nothing.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	u, err := url.Parse(strings.TrimSpace(c.Endpoint))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("observer endpoint %q must be an http(s) base URL", c.Endpoint)
	}
	if strings.TrimSpace(c.Model) == "" {
		return errors.New("observer model is required when the observer is enabled")
	}
	if c.MaxEvents < 1 {
		return errors.New("observer maxEvents must be at least 1")
	}
	if c.Timeout <= 0 {
		return errors.New("observer timeout must be positive")
	}
	return nil
}

// FileSettings converts a runtime configuration back into the file shape.
//
// Fields that equal the built-in defaults are left zero so a save from the
// interface neither materialises a long prompt into a config file nor pins
// defaults the operator never chose. A customised value survives a round trip.
func (c Config) FileSettings() Settings {
	settings := Settings{
		Enabled:   c.Enabled,
		Endpoint:  strings.TrimSpace(c.Endpoint),
		Model:     strings.TrimSpace(c.Model),
		APIKeyEnv: strings.TrimSpace(c.APIKeyEnv),
	}
	if prompt := strings.TrimSpace(c.SystemPrompt); prompt != "" && prompt != DefaultSystemPrompt {
		settings.SystemPrompt = prompt
	}
	if c.MaxEvents > 0 && c.MaxEvents != DefaultMaxEvents {
		settings.MaxEvents = c.MaxEvents
	}
	if c.Timeout > 0 && c.Timeout != DefaultTimeout {
		settings.TimeoutSeconds = int(c.Timeout / time.Second)
	}
	if c.MaxTokens > 0 && c.MaxTokens != DefaultMaxTokens {
		settings.MaxTokens = c.MaxTokens
	}
	return settings
}

// WithEnv applies the observer environment overrides to a configuration.
//
// The environment is the operator's last word for one run, so it has to be
// applied after anything a config file or the interface supplied — including a
// live save from the interface. Unset or unparsable values leave the
// configuration alone rather than resetting it.
func (c Config) WithEnv() Config {
	if raw, ok := os.LookupEnv("DUO_OBSERVER"); ok {
		if enabled, ok := parseEnvBool(raw); ok {
			c.Enabled = enabled
		}
	}
	if value := strings.TrimSpace(os.Getenv("DUO_OBSERVER_ENDPOINT")); value != "" {
		c.Endpoint = value
	}
	if value := strings.TrimSpace(os.Getenv("DUO_OBSERVER_MODEL")); value != "" {
		c.Model = value
	}
	return c
}

// EnvOverrides reports which settings the environment currently decides.
func EnvOverrides() (enabled, endpoint, model bool) {
	if raw, ok := os.LookupEnv("DUO_OBSERVER"); ok {
		_, enabled = parseEnvBool(raw)
	}
	return enabled,
		strings.TrimSpace(os.Getenv("DUO_OBSERVER_ENDPOINT")) != "",
		strings.TrimSpace(os.Getenv("DUO_OBSERVER_MODEL")) != ""
}

// parseEnvBool reads the truthy spellings Duo accepts everywhere else.
func parseEnvBool(raw string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	default:
		return false, false
	}
}

// APIKey resolves the key to send, or empty when none is configured.
//
// The raw DUO_OBSERVER_API_KEY in the process environment wins over the variable
// APIKeyEnv names, and both win over the owner-only key file the interface saves.
// That order lets a one-off run override a remembered key without editing
// anything, and keeps the file a convenience rather than the authority.
func (c Config) APIKey() string {
	if key := strings.TrimSpace(os.Getenv("DUO_OBSERVER_API_KEY")); key != "" {
		return key
	}
	if name := strings.TrimSpace(c.APIKeyEnv); name != "" {
		if key := strings.TrimSpace(os.Getenv(name)); key != "" {
			return key
		}
	}
	return readKeyFile()
}

// Observer keeps the recent session log and turns one human request into one
// model call. It runs no timer: a report exists only because a human asked.
type Observer struct {
	cfg    Config
	bus    *events.Bus
	live   func() string
	logf   func(string, ...any)
	key    string
	client *http.Client

	mu          sync.Mutex
	window      []events.Event
	previous    string
	cancel      context.CancelFunc
	unsubscribe func()
	closed      bool
	done        chan struct{}
}

// New validates cfg and prepares an observer. live renders the current runtime
// state at request time, and logf receives failures; neither may be nil.
func New(bus *events.Bus, cfg Config, live func() string, logf func(string, ...any)) (*Observer, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, errors.New("observer: not enabled")
	}
	if bus == nil {
		return nil, errors.New("observer: nil event bus")
	}
	if live == nil {
		return nil, errors.New("observer: nil live context provider")
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	o := &Observer{
		cfg:    cfg,
		bus:    bus,
		live:   live,
		logf:   logf,
		key:    cfg.APIKey(),
		client: &http.Client{Timeout: cfg.Timeout},
		done:   make(chan struct{}),
	}
	if o.key == "" {
		logf("observer: no API key in DUO_OBSERVER_API_KEY or %s; no request will be sent", cfg.APIKeyEnv)
	}
	return o, nil
}

// Start subscribes to the bus and begins keeping the recent log. It returns
// immediately; nothing is sent until Report is called.
func (o *Observer) Start(ctx context.Context) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed || o.cancel != nil {
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	o.cancel = cancel
	in, unsubscribe := o.bus.Subscribe(busBuffer)
	o.unsubscribe = unsubscribe
	go o.collect(runCtx, in)
}

// Close stops collecting and waits for the collector to exit. It is idempotent,
// and safe to call on an observer that was never started.
func (o *Observer) Close() {
	if o == nil {
		return
	}
	o.mu.Lock()
	if o.closed || o.cancel == nil {
		o.mu.Unlock()
		return
	}
	o.closed = true
	cancel, unsubscribe, done := o.cancel, o.unsubscribe, o.done
	o.cancel = nil
	o.unsubscribe = nil
	o.mu.Unlock()

	cancel()
	unsubscribe()
	<-done
}

// collect keeps the bounded recent log the next report reads.
func (o *Observer) collect(ctx context.Context, in <-chan events.Event) {
	defer close(o.done)
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-in:
			if !ok {
				return
			}
			if !observable(event) {
				continue
			}
			o.mu.Lock()
			o.window = append(o.window, event)
			o.window = trimWindow(o.window, o.cfg.MaxEvents)
			o.mu.Unlock()
		}
	}
}

// Report asks the model for one fresh status report and returns its text.
//
// The live block is sampled through the provider at call time, so a request that
// waited in a queue reports what is true when it runs rather than what was true
// when the human pressed the key. A truncated answer returns its text together
// with the diagnostic, because a partial report is still worth showing.
func (o *Observer) Report(ctx context.Context) (string, error) {
	if o == nil {
		return "", errors.New("observer: not running")
	}
	if o.key == "" {
		return "", fmt.Errorf("observer: no API key in DUO_OBSERVER_API_KEY or %s", o.cfg.APIKeyEnv)
	}
	live := strings.TrimSpace(o.live())
	o.mu.Lock()
	batch := append([]events.Event(nil), o.window...)
	previous := o.previous
	o.mu.Unlock()

	payload := map[string]any{
		"model": o.cfg.Model,
		"messages": []map[string]string{
			{"role": "system", "content": o.cfg.SystemPrompt},
			{"role": "user", "content": userPrompt(live, transcript(batch), previous)},
		},
	}
	if o.cfg.MaxTokens > 0 {
		payload["max_tokens"] = o.cfg.MaxTokens
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode request: %w", err)
	}

	endpoint := strings.TrimRight(o.cfg.Endpoint, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+o.key)

	resp, err := o.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("model returned %s: %s", resp.Status, clip(string(data), maxErrorBody))
	}

	var answer struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &answer); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if len(answer.Choices) == 0 {
		return "", errors.New("model returned no choices")
	}
	text := strings.TrimSpace(answer.Choices[0].Message.Content)
	if text == "" {
		return "", errors.New("model returned an empty report")
	}
	if answer.Choices[0].FinishReason == "length" {
		return text, errors.New("report truncated at maxTokens; raise observer.maxTokens")
	}
	// A truncated report is not the complete previous report, so it is not fed
	// back as one; only a clean answer becomes continuity for the next request.
	o.mu.Lock()
	o.previous = text
	o.mu.Unlock()
	return text, nil
}

// userPrompt assembles the three blocks a report is written from.
func userPrompt(live, log, previous string) string {
	var b strings.Builder
	b.WriteString("Current state (sampled live; the only source for what is happening now):\n")
	if live == "" {
		b.WriteString("(unavailable)\n")
	} else {
		b.WriteString(live)
		b.WriteString("\n")
	}
	b.WriteString("\nRecent session log (oldest first, newest last):\n")
	if strings.TrimSpace(log) == "" {
		b.WriteString("(no messages yet)\n")
	} else {
		b.WriteString(log)
	}
	if previous = strings.TrimSpace(previous); previous != "" {
		b.WriteString("\nPrevious report (continuity only; do not repeat its finished bullets):\n")
		b.WriteString(previous)
		b.WriteString("\n")
	}
	return b.String()
}

// observable reports whether an event belongs in the recent log.
//
// Kinds with no text (activity, model, thinking) carry nothing a prompt can
// quote: what an agent is doing at this instant comes from the live block, not
// from an empty line in the log.
func observable(event events.Event) bool {
	return strings.TrimSpace(event.Text) != ""
}

// trimWindow keeps the newest events within both the count and rune budgets.
func trimWindow(window []events.Event, maxEvents int) []events.Event {
	if maxEvents > 0 && len(window) > maxEvents {
		window = window[len(window)-maxEvents:]
	}
	total := 0
	for i := len(window) - 1; i >= 0; i-- {
		total += len([]rune(window[i].Text))
		if total > maxTranscriptRunes {
			return append([]events.Event(nil), window[i+1:]...)
		}
	}
	return window
}

// transcript renders a batch the way the model reads it: one line per event,
// speaker and kind first so the model can tell who did what.
func transcript(batch []events.Event) string {
	var b strings.Builder
	for _, event := range batch {
		who := string(event.Agent)
		if who == "" {
			who = "Duo"
		}
		if event.Peer != "" {
			who += " -> " + string(event.Peer)
		}
		kind := string(event.Kind)
		if kind == "" {
			kind = "event"
		}
		fmt.Fprintf(&b, "%s [%s]: %s\n", who, kind, clip(strings.TrimSpace(event.Text), maxEventRunes))
	}
	return b.String()
}

// clip shortens text to at most limit runes, marking the cut.
func clip(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}
