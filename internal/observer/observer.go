// Package observer is Duo's optional advisory observer.
//
// It watches the session's own event stream and asks an OpenAI-compatible model
// for a short observation about it. It is deliberately passive: it reads the
// existing event bus, whose Emit drops rather than blocks, and it hands its text
// back through a callback. It never writes workflow state, never talks to an
// agent and never owns a phase.
//
// An observer that is disabled, misconfigured, slow or broken must be
// indistinguishable from no observer at all, so every failure here is a log line
// and nothing else.
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
	// Default events kept for one request.
	DefaultMaxEvents = 20
	// Default quiet period after the last event before updating a report.
	DefaultDebounce = 3 * time.Second
	// Default per-request budget.
	DefaultTimeout = 60 * time.Second
	// Default answer length; progress reports have four required sections.
	DefaultMaxTokens = 800

	// silentReply is what the default prompt asks for when nothing is worth
	// reporting, so a quiet session produces no noise in the timeline.
	silentReply = "NOTHING"

	// busBuffer is large enough for a burst of tool activity. The bus drops when
	// a subscriber falls behind, which is the behaviour this package relies on.
	busBuffer = 256

	// maxEventRunes bounds one event's contribution to a prompt, and
	// maxErrorBody bounds how much of a failed response is logged.
	maxEventRunes = 1500
	maxErrorBody  = 512
)

// DefaultSystemPrompt asks for an incremental progress report, not an advisory
// critique. The previous report and only newly observed events are sent each time.
const DefaultSystemPrompt = "You are Duo's observer. Duo is a coding workflow where Austin and Tony " +
	"collaborate on a task. Update the running progress report using the previous report and the new " +
	"events. Return exactly four sections, in the human's language: completed work; what is in progress; " +
	"next steps explicitly stated by the agents; and blockers or decisions for the human. Always include " +
	"all four sections; when a section has no supported facts, write a brief equivalent of 'None stated'. " +
	"Do not use fixed English headings when the human uses another language. Keep the entire report to at " +
	"most 12 short bullets, with at most the five newest completed items. Merge or remove old completed " +
	"items instead of preserving an ever-growing history. Preserve only relevant facts, replace stale " +
	"status with newer events, and do not invent details or instruct the agents. Only describe future work " +
	"when the agents explicitly stated it. If the new events do not change the report, reply exactly: NOTHING"

// Config is the resolved observer configuration. It is what the session runs
// with; Settings is the file-facing shape.
type Config struct {
	Enabled      bool
	Endpoint     string
	Model        string
	APIKeyEnv    string
	SystemPrompt string
	MaxEvents    int
	Debounce     time.Duration
	Timeout      time.Duration
	MaxTokens    int
}

// Settings is the JSON shape of the observer section of a Duo config file.
//
// Durations are seconds because the file is hand-written. A key is never part of
// the file: APIKeyEnv names the environment variable that holds it, and the
// process environment's DUO_OBSERVER_API_KEY wins over that name.
type Settings struct {
	Enabled         bool   `json:"enabled,omitempty"`
	Endpoint        string `json:"endpoint,omitempty"`
	Model           string `json:"model,omitempty"`
	APIKeyEnv       string `json:"apiKeyEnv,omitempty"`
	SystemPrompt    string `json:"systemPrompt,omitempty"`
	MaxEvents       int    `json:"maxEvents,omitempty"`
	DebounceSeconds int    `json:"debounceSeconds,omitempty"`
	TimeoutSeconds  int    `json:"timeoutSeconds,omitempty"`
	MaxTokens       int    `json:"maxTokens,omitempty"`
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
		Debounce:     time.Duration(s.DebounceSeconds) * time.Second,
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
// so the observer never runs with a zero debounce or a missing prompt.
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
	if c.Debounce <= 0 {
		c.Debounce = DefaultDebounce
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
	if c.Debounce <= 0 {
		return errors.New("observer debounce must be positive")
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
	if c.Debounce > 0 && c.Debounce != DefaultDebounce {
		settings.DebounceSeconds = int(c.Debounce / time.Second)
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

// Observer turns a burst of session events into at most one model request per
// quiet period, and reports the answer through emit.
type Observer struct {
	cfg    Config
	bus    *events.Bus
	emit   func(string)
	logf   func(string, ...any)
	key    string
	client *http.Client

	results chan result

	mu          sync.Mutex
	cancel      context.CancelFunc
	unsubscribe func()
	closed      bool
	done        chan struct{}
}

type result struct {
	text string
	err  error
}

// New validates cfg and prepares an observer. emit receives every observation
// that is worth showing; logf receives failures. Both may be called from the
// observer's own goroutine.
func New(bus *events.Bus, cfg Config, emit func(string), logf func(string, ...any)) (*Observer, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, errors.New("observer: not enabled")
	}
	if bus == nil {
		return nil, errors.New("observer: nil event bus")
	}
	if emit == nil {
		return nil, errors.New("observer: nil emit")
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	o := &Observer{
		cfg:     cfg,
		bus:     bus,
		emit:    emit,
		logf:    logf,
		key:     cfg.APIKey(),
		client:  &http.Client{Timeout: cfg.Timeout},
		results: make(chan result, 1),
		done:    make(chan struct{}),
	}
	if o.key == "" {
		logf("observer: no API key in DUO_OBSERVER_API_KEY or %s; no request will be sent", cfg.APIKeyEnv)
	}
	return o, nil
}

// Start subscribes to the bus and begins observing. It returns immediately; the
// first observation appears once a burst of events has gone quiet.
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
	go o.loop(runCtx, in)
}

// Close stops observing and waits for the loop to exit. It is idempotent, and
// safe to call on an observer that was never started.
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

func (o *Observer) loop(ctx context.Context, in <-chan events.Event) {
	defer close(o.done)

	var (
		pending  []events.Event
		inFlight bool
		report   string
		lastEmit string
		timer    *time.Timer
		timerC   <-chan time.Time
	)
	armTimer := func() {
		if timer == nil {
			timer = time.NewTimer(o.cfg.Debounce)
		} else {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(o.cfg.Debounce)
		}
		timerC = timer.C
	}
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

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
			pending = append(pending, event)
			if len(pending) > o.cfg.MaxEvents {
				pending = append([]events.Event(nil), pending[len(pending)-o.cfg.MaxEvents:]...)
			}
			if !inFlight {
				armTimer()
			}
		case <-timerC:
			timerC = nil
			if inFlight || len(pending) == 0 {
				continue
			}
			batch := pending
			pending = nil
			inFlight = true
			previous := report
			go func() { o.results <- o.ask(ctx, previous, batch) }()
		case res := <-o.results:
			inFlight = false
			if text := strings.TrimSpace(res.text); text != "" && !strings.EqualFold(text, silentReply) {
				if text != lastEmit {
					lastEmit = text
					o.emit(text)
				}
				// Do not feed a known-truncated report back as the complete prior
				// report on the next incremental request.
				if res.err == nil {
					report = text
				}
			}
			// A truncated response carries both useful text and a diagnostic. Emit
			// first because a successful report clears a stale notice; then leave
			// the current truncation notice visible to the frontend.
			if res.err != nil {
				o.logf("observer: %v", res.err)
			}
			if len(pending) > 0 {
				armTimer()
			}
		}
	}
}

// observable reports whether an event belongs in a prompt.
//
// The observer's own output is excluded explicitly: it is published on the same
// bus, so accepting it would make the observer ask the model about its previous
// answer and emit again, forever. Kinds with no text (activity, model, thinking)
// carry nothing to observe and are skipped for the same reason a blank line is.
func observable(event events.Event) bool {
	if event.Kind == events.KindObserver {
		return false
	}
	return strings.TrimSpace(event.Text) != ""
}

// ask makes one OpenAI-compatible chat completion request for the batch.
func (o *Observer) ask(ctx context.Context, previous string, batch []events.Event) result {
	if o.key == "" {
		// Already reported by New; staying silent here keeps the log to one line.
		return result{}
	}

	userPrompt := "New events since the previous report:\n" + transcript(batch)
	if strings.TrimSpace(previous) != "" {
		userPrompt = "Previous running report:\n" + previous + "\n\n" + userPrompt
	}
	payload := map[string]any{
		"model": o.cfg.Model,
		"messages": []map[string]string{
			{"role": "system", "content": o.cfg.SystemPrompt},
			{"role": "user", "content": userPrompt},
		},
	}
	if o.cfg.MaxTokens > 0 {
		payload["max_tokens"] = o.cfg.MaxTokens
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return result{err: fmt.Errorf("encode request: %w", err)}
	}

	endpoint := strings.TrimRight(o.cfg.Endpoint, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return result{err: fmt.Errorf("build request: %w", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+o.key)

	resp, err := o.client.Do(req)
	if err != nil {
		return result{err: fmt.Errorf("request %s: %w", endpoint, err)}
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return result{err: fmt.Errorf("read response: %w", err)}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return result{err: fmt.Errorf("model returned %s: %s", resp.Status, clip(string(data), maxErrorBody))}
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
		return result{err: fmt.Errorf("decode response: %w", err)}
	}
	if len(answer.Choices) == 0 {
		return result{err: errors.New("model returned no choices")}
	}
	text := strings.TrimSpace(answer.Choices[0].Message.Content)
	if answer.Choices[0].FinishReason == "length" {
		return result{text: text, err: errors.New("report truncated at maxTokens; raise observer.maxTokens")}
	}
	return result{text: text}
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
