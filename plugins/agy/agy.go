// Package agy implements the Agy driver for Duo Core.
//
// Agy has no extension API, so this package is both halves of the plugin at once:
// the Host Adapter that answers the protocol (see Describe/Prepare below) and the
// Agent Adapter that observes the agent (see watcher.go). Neither half is visible
// to Core beyond two capabilities: agy reports activity, and its bridge endpoint
// belongs to the plugin rather than to the agent process.
package agy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/internal/protocol"
)

// Name is the driver name an operator selects with `--agent agy`.
const Name = "agy"

// DriverVersion is this plugin's version, independent of the Duo binary version.
const DriverVersion = "1.0.0"

// sessionFlags are the flags through which an operator may already have chosen a
// conversation. Detection is token-exact: a substring test for "-c" also matches
// "--config", which once cost an agent its conversation.
var sessionFlags = []string{"--conversation", "-c"}

// state is the plugin's private resume blob. Core stores it verbatim and never
// reads it.
type state struct {
	// ConversationID is the conversation agy reported. It is spelled with agy's
	// own word so the blob is readable by whoever debugs it.
	ConversationID string `json:"conversationId,omitempty"`
	// SessionID is the same identity under the one word every driver shares. Duo
	// Core migrates a pre-protocol snapshot into exactly this spelling without
	// knowing which agent it belongs to, so this driver has to accept it: Core is
	// not allowed to know that agy calls it a conversation. It is only ever read,
	// never written.
	SessionID string `json:"sessionId,omitempty"`
	// LogFile is where this launch's agy run records the conversation it opened.
	// The Agent Adapter reads it to learn the id, because agy only reports it
	// once it has one. It has to survive a resume, or the adapter re-reads the
	// wrong file.
	LogFile string `json:"logFile,omitempty"`
}

// conversation returns the conversation to replay, preferring this plugin's own
// spelling and falling back to the shared one. A blob carrying both is ambiguous,
// and the plugin's own field wins because it is the one it wrote.
func (s state) conversation() string {
	if s.ConversationID != "" {
		return s.ConversationID
	}
	return s.SessionID
}

// Observe receives the activity this plugin infers from watching agy. Core
// supplies it while the driver runs in-process; an out-of-process plugin reports
// the same messages over Duo's bridge instead.
type Observe func(protocol.AgentID, protocol.Message)

// Plugin is the Agy Host Adapter and Agent Adapter.
type Plugin struct {
	observe Observe

	mu      sync.Mutex
	watcher *AgyWatcher
}

// New returns the Agy driver. observe may be nil, in which case no activity is
// inferred; the agent still runs and still reports through Duo's PTY fallback.
func New(observe Observe) driver.Handler { return &Plugin{observe: observe} }

// Register makes Agy available to Duo in-process.
func Register(observe Observe) {
	driver.Register(Name, func() driver.Handler { return New(observe) })
}

func (p *Plugin) Describe() (*driver.Manifest, error) {
	return &driver.Manifest{
		Protocol:    driver.ProtocolVersion,
		Name:        Name,
		Version:     DriverVersion,
		Description: "Google Antigravity CLI (transcript observation)",
		Agent:       driver.AgentInfo{CLI: Name, DefaultCommand: Name},
		// Agy names a model by its bare id and rejects a provider prefix.
		DefaultModel:   DefaultModel,
		ModelReference: driver.ModelBare,
		Capabilities: driver.Capabilities{
			// Agy answers a conversation id it has never seen with "not found,
			// ignoring --conversation flag" and opens a different conversation, so
			// it owns the identity and must not be handed one of Duo's. This is
			// ResumeServer, and it is why the rejected value must never be
			// persisted: doing so loses the conversation on every launch.
			Resume: driver.ResumeServer,
			Models: true,
			// The effort is a launch flag, and the picker applies it by restarting
			// the agent.
			Thinking: true,
			// Neither model nor effort reaches a running agy: both are startup
			// flags, so Core restarts the process to apply them.
			LiveModelSwitch:    false,
			LiveThinkingSwitch: false,
			Activity:           true,
			ContextUsage:       true,
			TokenRate:          true,
			// This plugin process holds the bridge endpoint, because agy has no
			// extension API and nothing inside the agent can connect. That endpoint
			// observes agy from the outside and cannot write to the PTY Core owns,
			// which is why liveSteering is false: Core delivers prompts to the
			// terminal, exactly as it did before.
			Bridge:       driver.BridgePlugin,
			MCP:          true,
			PTYFallback:  true,
			LiveSteering: false,
			// The endpoint cannot attach until agy has told us which conversation
			// it opened, so Core announces the connection at process start.
			SelfReports: false,
		},
	}, nil
}

func (p *Plugin) Probe() (*driver.ProbeResult, error) {
	path, err := LookPath(Name)
	if err != nil {
		return &driver.ProbeResult{Available: false, Reason: "the agy CLI is not on PATH"}, nil
	}
	return &driver.ProbeResult{Available: true, AgentPath: path}, nil
}

// Prepare builds the command line, performs the one piece of agy setup that has
// to happen before launch, and reports what the operator should know about this
// machine.
//
// A trusted-workspaces entry is written here rather than by Core because it is
// agy's settings.json, not Duo's state: Core has no business editing an agent's
// configuration, and a fourth driver will have its own equivalent.
func (p *Plugin) Prepare(req driver.LaunchRequest) (*driver.LaunchPlan, error) {
	var prior state
	if len(req.State) > 0 {
		if err := json.Unmarshal(req.State, &prior); err != nil {
			return nil, &driver.RPCError{
				Code:    driver.CodeInvalid,
				Message: "agy resume state is not readable: " + err.Error(),
			}
		}
	}
	conversation := prior.conversation()

	// An agent runs unattended in its own worktree, so the interactive workspace
	// trust prompt must not block it.
	if err := EnsureWorkspaceTrusted(req.CWD, req.RepositoryRoot); err != nil {
		return nil, err
	}

	base := strings.TrimSpace(req.BaseCommand)
	if base == "" {
		base = Name
	}

	command := setModel(base, strings.TrimSpace(req.Model))
	command = setEffort(command, strings.TrimSpace(req.Thinking))

	parts := []string{command}
	// Only a conversation agy has actually reported is passed. Sending the
	// placeholder identity this session computed buys nothing and produces a
	// misleading warning on every launch, so the Agent Adapter learns the real id
	// from agy's log and the next launch replays it.
	if conversation != "" && !driver.HasFlag(command, sessionFlags...) {
		parts = append(parts, `--conversation "$DUO_AGY_CONVERSATION_ID"`)
	}
	if !strings.Contains(command, "--dangerously-skip-permissions") {
		parts = append(parts, "--dangerously-skip-permissions")
	}

	logFile := prior.LogFile
	// A stale log from an earlier run must never be read as this run's: the
	// adapter learns the conversation id from it. The name is stable across runs
	// so a resume keeps reading the same file, and carries the agent so two
	// agents never watch each other's conversation.
	if logFile == "" {
		who := strings.ToLower(strings.TrimSpace(string(req.Agent)))
		if who == "" {
			who = "agent"
		}
		logFile = filepath.Join(os.TempDir(), fmt.Sprintf("duo-agy-%s.log", who))
	}
	if !strings.Contains(command, "--log-file") {
		parts = append(parts, "--log-file "+driver.ShellQuote(logFile))
	}

	next := state{ConversationID: conversation, LogFile: logFile}
	raw, err := json.Marshal(next)
	if err != nil {
		return nil, err
	}

	plan := &driver.LaunchPlan{
		Command: strings.Join(parts, " "),
		Env: map[string]string{
			"DUO_AGY_CONVERSATION_ID": conversation,
			"DUO_AGY_LOG_FILE":        logFile,
		},
		State:           raw,
		SessionIdentity: conversation,
		// Core clears these before the next launch so no stale value is read.
		Cleanup: []string{logFile},
	}

	// Checked here, not in describe: sqlite3 may be installed between the two, and
	// a notice that is true when it was written is not true when it is read.
	if !HasSqlite3() {
		plan.Notices = append(plan.Notices, Sqlite3MissingNotice)
	}

	p.startObserver(req, conversation, next)
	return plan, nil
}

// startObserver begins watching agy's transcript for this launch. It runs in the
// plugin, so Core never learns that agy is observed at all: from Core's side the
// only difference is that `activity` is true.
func (p *Plugin) startObserver(req driver.LaunchRequest, conversation string, st state) {
	if p.observe == nil {
		return
	}
	transcript := TranscriptPath(conversation)

	// On a first run there is no conversation yet, so the transcript does not
	// exist. The adapter learns the id from agy's log first.
	if transcript == "" || !fileExists(transcript) {
		go func() {
			id := waitForConversationID(st.LogFile)
			if id == "" {
				return
			}
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.watcher != nil {
				return
			}
			p.watcher = NewAgyWatcher(protocol.AgentID(req.Agent), TranscriptPath(id), protocolFuncSink(p.observe))
			p.watcher.Start(context.Background())
		}()
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.watcher != nil {
		p.watcher.Stop()
	}
	p.watcher = NewAgyWatcher(protocol.AgentID(req.Agent), transcript, protocolFuncSink(p.observe))
	p.watcher.Start(context.Background())
}

// Close stops the observer. An out-of-process plugin reaches this when Core closes
// the plugin; a built-in driver reaches it when the session ends.
func (p *Plugin) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.watcher != nil {
		p.watcher.Stop()
		p.watcher = nil
	}
}

// ModelOptions are agy's reasoning-effort levels, in escalation order.
var ModelOptions = []string{"off", "low", "medium", "high", "max"}

func (p *Plugin) Models(ctx context.Context) (*driver.ModelList, error) {
	return driver.RunModelList(ctx, ListCommand(), ParseModels)
}

func (p *Plugin) Thinking(ctx context.Context) (*driver.ThinkingOptions, error) {
	return &driver.ThinkingOptions{Levels: ModelOptions, Default: "medium"}, nil
}

// setModel applies agy's model flag. Agy takes a bare id, so a provider prefix is
// stripped: the operator may well have written "vendor/model" out of habit, and
// agy would abort on it.
func setModel(base, model string) string {
	if model = driver.BareModelID(model); model != "" {
		return driver.SetFlag(base, "--model", model)
	}
	// No model was selected, but the operator's own command may still carry a
	// provider-prefixed --model. Normalise it in place so agy sees the bare name.
	if existing, ok := driver.ReadFlag(base, "--model"); ok {
		return driver.SetFlag(base, "--model", driver.BareModelID(existing))
	}
	return base
}

func setEffort(base, effort string) string {
	if effort = strings.TrimSpace(effort); effort == "" {
		return base
	}
	return driver.SetFlag(base, "--effort", effort)
}

// protocolFuncSink adapts an Observe to the watcher's sink.
type protocolFuncSink func(protocol.AgentID, protocol.Message)

func (f protocolFuncSink) OnActivity(agent protocol.AgentID, msg protocol.Message) {
	f(agent, msg)
}
