// Package observer is Duo's optional advisory observer.
//
// It watches the session's own event stream and, when a human asks for one
// status report, asks an OpenAI-compatible model for it. It is deliberately
// passive: it reads the existing event bus, whose Emit drops rather than blocks,
// it runs no timer, and it never writes workflow state, talks to an agent or owns
// a phase.
//
// One narrower request exists because a human has to be able to find a session
// again: Title names a session after the first task it was given. It returns a
// value and nothing else — the session, not the observer, decides to record it.
//
// A report answers four human questions: what the task is and how far its
// decomposition has come, what just finished, what is happening right now, and
// what comes next. The task and the decomposition cannot come from the event
// stream either — the original request scrolls out of the window and the plan
// lives in workflow state, not in messages — so the task is remembered
// separately and the plan and step queue arrive in the live block. "Right now"
// cannot come from the event stream at all, because a running tool is not a
// message, so the caller supplies a live context provider: a block sampled from
// Duo's runtime at request time.
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
	// Default answer length; a status report has four required sections plus an
	// optional fifth, and a direct answer to a question has to fit in the same
	// budget. Too small a budget shows up as a truncated report, which is worse
	// than a few unused tokens.
	DefaultMaxTokens = 1000

	// DefaultEndpoint and DefaultModel are what Duo offers a human who has never
	// configured the observer: a local OpenAI-compatible gateway and the model
	// name such a gateway commonly serves. Both the dashboard's setup panel and
	// the first-run guide offer them, so they live here rather than in either
	// frontend.
	DefaultEndpoint = "http://127.0.0.1:4000/v1"
	DefaultModel    = "exec"

	// busBuffer is large enough for a burst of tool activity. The bus drops when
	// a subscriber falls behind, which is the behaviour this package relies on.
	busBuffer = 256

	// maxEventRunes bounds one event's contribution to a prompt,
	// maxTranscriptRunes bounds the whole recent log so a chatty session cannot
	// price a human's Ctrl+E out of reach, maxTaskRunes bounds the remembered
	// original task so one long human prompt cannot dominate every later report,
	// and maxErrorBody bounds how much of a failed response is logged.
	maxTaskRunes       = 2000
	maxEventRunes      = 1200
	maxTranscriptRunes = 6000
	maxErrorBody       = 512
	// maxQuestionRunes bounds the question a human types into the report panel,
	// so one long pasted question cannot dominate the prompt.
	maxQuestionRunes = 500

	// titleMaxTokens is deliberately small: a session name is a few words, and a
	// title that runs into the cap is rejected rather than trimmed.
	titleMaxTokens = 64
	// maxTitleRunes bounds the session name a frontend has to lay out, so one
	// verbose model answer cannot overflow a picker row.
	maxTitleRunes = 60
)

// titleSystemPrompt asks for one short session name. It is separate from the
// report prompt because naming a session is a different question: the report
// prompt is about the work's state, and asking it for a title would spend a
// report's worth of tokens on a name.
const titleSystemPrompt = "You name Duo sessions. Duo is a coding workflow where two agents work on one task. " +
	"Given the human's task, reply with one short title for the session that names what it is about: three to eight " +
	"words, in the same language as the task, on a single line, without quotes, without markdown, and without a " +
	"trailing full stop. Output the title only and nothing else."

// DefaultSystemPrompt asks for a status report about the task and the agents,
// and for a direct answer when the human asks the observer a question.
//
// The four sections are the human's own questions. Progress comes first because
// it is the question the timeline answers worst: a report that only lists recent
// messages cannot say how many steps the work was split into or how many are
// done, so it is pinned to the Task block and the live plan and step queue —
// state, not a log. The "Doing now" rule is the part that makes the rest of the
// report worth opening: it may only use the live block, so a finished tool can
// never be presented as work in progress, which is exactly what an event-only
// observer used to get wrong. "Watch out" exists because the observer sees both
// what the agents say and what the evidence shows; it is optional so a healthy
// session is never padded with invented concerns.
const DefaultSystemPrompt = "You are Duo's observer. Duo is a coding workflow where two agents, Austin and Tony, " +
	"work on one task in separate Git worktrees. You write a short status report for the human watching them, in " +
	"the human's language, including the section headings. Each request carries four blocks, in this order: 'Task' " +
	"(the human's original request for this round, verbatim); 'Current state' (sampled live right now from Duo's " +
	"runtime: mode, phase, the shared plan text, the step queue with which steps have landed, the current step, " +
	"each agent's sign-off with its note or evidence, the Fast-mode verification verdict, each agent's worktree " +
	"with its branch, short head, commits ahead of the base commit and whether it has uncommitted changes, the " +
	"files each branch changed and which of those both branches touched, the session's token and cost totals, " +
	"the test command, and per agent whether it is busy or idle, the tool it is running with its arguments and " +
	"elapsed time, how long the current turn has lasted, the last few finished tools, the text it is currently " +
	"writing, and its context usage); 'Recent session log' (the newest messages between the agents, the human and " +
	"Duo); and 'Previous report' (your last report, for continuity only).\n\n" +
	"When a request also carries a 'Question from the human' block, do not write the report: answer that question " +
	"directly, in the human's language, in at most eight short lines, using the same blocks as your evidence. The " +
	"question is the human asking you to look at something specific, not a request to repeat the report.\n\n" +
	"Otherwise write these sections in this order. The first four are required; the fifth is optional:" +
	"\n" +
	"1. Progress - the task in one sentence (from the Task block), then where the work stands in the task's own " +
	"decomposition: how many steps the work was split into, how many are done, which step is current, and what " +
	"remains. Everything here comes ONLY from the Current state block (plan text and step queue); never infer " +
	"progress from the log. In Step mode the step queue is the decomposition. In Goal mode the shared plan's " +
	"numbered items are the decomposition. When neither exists, say what phase the workflow is in instead, in " +
	"one line.\n" +
	"2. Just finished - at most three bullets, newest first, each a concrete result, kept relevant to the " +
	"current step or phase. Never repeat a bullet from the previous report, never accumulate history, and drop " +
	"anything newer events have superseded.\n" +
	"3. Doing now - one line per agent, derived ONLY from the Current state block, never guessed from the log. " +
	"Name the agent, say whether it is busy or idle, and say what it is doing: the running tool with its " +
	"arguments and how long it has run, the file it is editing, or the first words of the text it is currently " +
	"writing. An idle agent is reported as idle together with what it finished last. Never describe a finished " +
	"tool as running.\n" +
	"4. Next - only what the agents explicitly stated they will do, in their order, at most three bullets. When " +
	"they stated nothing, say so in one short line.\n" +
	"5. Watch out - at most two bullets, and only when the Current state block or the log shows a concrete " +
	"problem the agents may not have noticed: a claim with no evidence behind it (an agent says work is done or " +
	"tests pass, while the log shows a failing tool, or its worktree has no commits ahead of the base), the same " +
	"tool failing repeatedly, both agents idle in a phase that needs one of them, a gate nobody can pass because " +
	"each agent is waiting for the other, or both branches changing the same files so integration will conflict. " +
	"Each bullet states the risk and names the evidence for it. When nothing concrete is visible, omit this " +
	"section and its heading entirely; never fill it with generic advice, style opinions or speculation.\n\n" +
	"End with one final line starting with the human's words for 'Needs your decision:' only when a decision " +
	"genuinely blocks the agents; otherwise omit that line entirely. Do not invent facts, do not advise the " +
	"agents, do not repeat a bullet twice, and keep the whole report to at most twenty short lines. An answer " +
	"to a question stays within the eight lines allowed above and never ends with a report section."

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

	mu           sync.Mutex
	window       []events.Event
	previous     string
	lastUserTask string
	cancel       context.CancelFunc
	unsubscribe  func()
	closed       bool
	done         chan struct{}
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

// collect keeps the bounded recent log the next report reads, and remembers the
// most recent human message separately: the original task anchors every report,
// but it scrolls out of the event window long before the work is done.
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
			if event.Kind == events.KindUser {
				o.lastUserTask = clip(strings.TrimSpace(event.Text), maxTaskRunes)
			}
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
	return o.ask(ctx, "")
}

// Answer asks the model one question the human typed, using the same blocks a
// report is written from, and returns its answer.
//
// It shares the report path on purpose: one in-flight call, one previous-report
// continuity field, one failure mode. Only the prompt differs, so a question can
// reach the evidence a plain report never mentions without a second code path to
// keep honest.
func (o *Observer) Answer(ctx context.Context, question string) (string, error) {
	question = NormalizeQuestion(question)
	if question == "" {
		return "", errors.New("observer: empty question")
	}
	return o.ask(ctx, question)
}

// NormalizeQuestion makes a typed question safe to put in a prompt line: it is
// trimmed, every run of whitespace becomes one space so it stays a single line,
// and it is bounded. Both frontends and the observer itself use this one rule, so
// a question cannot render one way in a panel and another in the prompt.
func NormalizeQuestion(question string) string {
	return clip(strings.Join(strings.Fields(question), " "), maxQuestionRunes)
}

// ask performs the one model call both Report and Answer use. An empty question
// means "write the report".
func (o *Observer) ask(ctx context.Context, question string) (string, error) {
	if o == nil {
		return "", errors.New("observer: not running")
	}
	live := strings.TrimSpace(o.live())
	o.mu.Lock()
	batch := append([]events.Event(nil), o.window...)
	previous := o.previous
	task := o.lastUserTask
	o.mu.Unlock()

	text, finishReason, err := o.complete(ctx, o.cfg.SystemPrompt, userPrompt(task, live, transcript(batch), previous, question), o.cfg.MaxTokens)
	if err != nil {
		return "", err
	}
	if text == "" {
		return "", errors.New("model returned an empty report")
	}
	if finishReason == "length" {
		return text, errors.New("report truncated at maxTokens; raise observer.maxTokens")
	}
	// A truncated report is not the complete previous report, so it is not fed
	// back as one; only a clean answer becomes continuity for the next request.
	o.mu.Lock()
	o.previous = text
	o.mu.Unlock()
	return text, nil
}

// Title asks the model for a short human-facing name for this session, derived
// from the task the human just submitted.
//
// The task is passed in rather than read from the collected log: a title is
// requested as soon as the human submits, and the user event may not have reached
// the collector yet. The title is only a suggestion — it is returned as a value
// and never touches workflow state — so a failure here is silent: the session is
// simply displayed by its id, exactly as a session with no observer is.
func (o *Observer) Title(ctx context.Context, task string) (string, error) {
	if o == nil {
		return "", errors.New("observer: not running")
	}
	task = clip(strings.TrimSpace(task), maxTaskRunes)
	if task == "" {
		return "", errors.New("observer: empty task")
	}
	text, finishReason, err := o.complete(ctx, titleSystemPrompt, "Task:\n"+task+"\n", titleMaxTokens)
	if err != nil {
		return "", err
	}
	if finishReason == "length" {
		return "", errors.New("title truncated at maxTokens")
	}
	title := NormalizeTitle(text)
	if title == "" {
		return "", errors.New("model returned an empty title")
	}
	return title, nil
}

// NormalizeTitle makes a model answer safe to show as a session name: it keeps
// the first non-empty line, drops the decoration models like to add (quotes,
// backticks, markdown heading or bullet marks, a trailing full stop) and bounds
// the result. One rule for every frontend, so the same title cannot render one
// way in /sessions and another in `duo sessions`.
func NormalizeTitle(raw string) string {
	line := ""
	for _, candidate := range strings.Split(raw, "\n") {
		if line = strings.TrimSpace(candidate); line != "" {
			break
		}
	}
	line = strings.TrimSpace(strings.TrimLeft(line, "#*-–—• "))
	line = strings.Trim(strings.TrimSpace(line), "\"'`“”‘’「」")
	line = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), "."))
	return clip(strings.Join(strings.Fields(line), " "), maxTitleRunes)
}

// complete performs one chat-completions call and returns the answer text with
// the model's finish reason. It is the single place a request is built and sent,
// so reports, questions and titles share one transport, one key and one failure
// mode. Empty text is not an error here: the caller knows what it asked for and
// can name the missing answer itself.
func (o *Observer) complete(ctx context.Context, system, user string, maxTokens int) (string, string, error) {
	if o == nil {
		return "", "", errors.New("observer: not running")
	}
	if o.key == "" {
		return "", "", fmt.Errorf("observer: no API key in DUO_OBSERVER_API_KEY or %s", o.cfg.APIKeyEnv)
	}

	payload := map[string]any{
		"model": o.cfg.Model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
	}
	if maxTokens > 0 {
		payload["max_tokens"] = maxTokens
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", "", fmt.Errorf("encode request: %w", err)
	}

	endpoint := strings.TrimRight(o.cfg.Endpoint, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+o.key)

	resp, err := o.client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("request %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", "", fmt.Errorf("model returned %s: %s", resp.Status, clip(string(data), maxErrorBody))
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
		return "", "", fmt.Errorf("decode response: %w", err)
	}
	if len(answer.Choices) == 0 {
		return "", "", errors.New("model returned no choices")
	}
	return strings.TrimSpace(answer.Choices[0].Message.Content), answer.Choices[0].FinishReason, nil
}

// userPrompt assembles the four blocks a report is written from, plus the
// human's question when there is one. The task comes first because it anchors the
// Progress section: everything after it is the present, while the task is why the
// work is being done at all. The question comes last because it is the one thing
// the answer has to address.
func userPrompt(task, live, log, previous, question string) string {
	var b strings.Builder
	b.WriteString("Task (the human's original request for this round):\n")
	if strings.TrimSpace(task) == "" {
		b.WriteString("(not seen yet)\n")
	} else {
		b.WriteString(task)
		b.WriteString("\n")
	}
	b.WriteString("\nCurrent state (sampled live; the only source for what is happening now and for progress in the task's decomposition):\n")
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
	if question != "" {
		b.WriteString("\nQuestion from the human (answer this instead of writing the report):\n")
		b.WriteString(question)
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
	// A timeline entry is a projection of an event this window already holds (the
	// raw event, or the pair it was drawn as), so reading it too would duplicate
	// every line the observer sees.
	return event.Kind != events.KindTimeline && strings.TrimSpace(event.Text) != ""
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
