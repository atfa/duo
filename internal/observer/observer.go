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
	// Default quiet period after the last event before the model is asked.
	DefaultDebounce = 3 * time.Second
	// Default per-request budget.
	DefaultTimeout = 60 * time.Second
	// Default answer length; an observation is a few sentences, not an essay.
	DefaultMaxTokens = 300

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

// DefaultSystemPrompt is the observer's own instruction. The operator may replace
// it, but the default asks for the one thing an advisory observer is for: a few
// concrete sentences, or silence.
const DefaultSystemPrompt = "You are Duo's observer. Duo is a coding workflow where two AI agents, " +
	"Austin and Tony, collaborate with a human. You are shown a short excerpt of their session events. " +
	"Reply with at most three sentences naming a concrete risk, contradiction, unverified claim or scope " +
	"creep worth the human's attention. Do not issue instructions to the agents and do not repeat what " +
	"the excerpt already says. If nothing is worth saying, reply exactly: NOTHING"

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

// Normalized trims string settings and fills tuning defaults, while preserving
// an empty endpoint/model so validation can explain a missing value.
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

// APIKey resolves the key to send, or empty when none is configured.
//
// The raw DUO_OBSERVER_API_KEY wins over the named environment variable, which
// wins over the owner-only key file.
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

// FileSettings converts the runtime settings to the persisted non-secret shape.
// Built-in defaults are omitted to keep the project file concise.
func (c Config) FileSettings() Settings {
	s := Settings{Enabled: c.Enabled, Endpoint: strings.TrimSpace(c.Endpoint), Model: strings.TrimSpace(c.Model), APIKeyEnv: strings.TrimSpace(c.APIKeyEnv)}
	if c.SystemPrompt != "" && c.SystemPrompt != DefaultSystemPrompt {
		s.SystemPrompt = c.SystemPrompt
	}
	if c.MaxEvents > 0 && c.MaxEvents != DefaultMaxEvents {
		s.MaxEvents = c.MaxEvents
	}
	if c.Debounce > 0 && c.Debounce != DefaultDebounce {
		s.DebounceSeconds = int(c.Debounce / time.Second)
	}
	if c.Timeout > 0 && c.Timeout != DefaultTimeout {
		s.TimeoutSeconds = int(c.Timeout / time.Second)
	}
	if c.MaxTokens > 0 && c.MaxTokens != DefaultMaxTokens {
		s.MaxTokens = c.MaxTokens
	}
	return s
}

// WithEnv applies explicit process overrides after file or UI settings.
func (c Config) WithEnv() Config {
	if raw, ok := os.LookupEnv("DUO_OBSERVER"); ok {
		if enabled, valid := parseEnvBool(raw); valid {
			c.Enabled = enabled
		}
	}
	if v := strings.TrimSpace(os.Getenv("DUO_OBSERVER_ENDPOINT")); v != "" {
		c.Endpoint = v
	}
	if v := strings.TrimSpace(os.Getenv("DUO_OBSERVER_MODEL")); v != "" {
		c.Model = v
	}
	return c
}

// EnvOverrides reports whether valid environment values control observer settings.
func EnvOverrides() (enabled, endpoint, model bool) {
	if raw, ok := os.LookupEnv("DUO_OBSERVER"); ok {
		_, enabled = parseEnvBool(raw)
	}
	return enabled, strings.TrimSpace(os.Getenv("DUO_OBSERVER_ENDPOINT")) != "", strings.TrimSpace(os.Getenv("DUO_OBSERVER_MODEL")) != ""
}

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
		timer    *time.Timer
		timerC   <-chan time.Time
		inFlight bool
	)

	armTimer := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(o.cfg.Debounce)
		timerC = timer.C
	}
	timer = time.NewTimer(o.cfg.Debounce)
	timer.Stop()
	defer timer.Stop()

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
			go func() {
				o.results <- o.ask(ctx, batch)
			}()
		case res := <-o.results:
			inFlight = false
			if res.err != nil {
				o.logf("observer: %v", res.err)
			}
			if text := strings.TrimSpace(res.text); text != "" && !strings.EqualFold(text, silentReply) {
				o.emit(text)
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
func (o *Observer) ask(ctx context.Context, batch []events.Event) result {
	if o.key == "" {
		// Already reported by New; staying silent here keeps the log to one line.
		return result{}
	}

	payload := map[string]any{
		"model": o.cfg.Model,
		"messages": []map[string]string{
			{"role": "system", "content": o.cfg.SystemPrompt},
			{"role": "user", "content": transcript(batch)},
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
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &answer); err != nil {
		return result{err: fmt.Errorf("decode response: %w", err)}
	}
	if len(answer.Choices) == 0 {
		return result{err: errors.New("model returned no choices")}
	}
	return result{text: strings.TrimSpace(answer.Choices[0].Message.Content)}
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
