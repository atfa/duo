package coordinator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	agentpkg "github.com/atfa/duo/internal/agent"
	"github.com/atfa/duo/internal/delivery"
	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/recovery"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/atfa/duo/internal/transport"
	"github.com/atfa/duo/internal/workspace"
)

type Coordinator struct {
	server    *transport.Server
	agents    *agentpkg.Manager
	project   *project.State
	tracker   *harness.Tracker
	workspace workspace.Manager
	bus       *events.Bus

	integrationMu sync.RWMutex
	integration   workspace.IntegrationResult

	deliveryMu sync.RWMutex
	delivery   sessionstore.Delivery
	// deliveryTxnMu serializes the complete handoff transaction for this
	// coordinator/session. Delivery is rare, so a session-wide lock is enough.
	deliveryTxnMu sync.Mutex

	testCommandMu sync.RWMutex
	testCommand   string
	language      string

	// contextWarnPercent is the context occupancy at which Duo tells an agent its
	// window is nearly full; zero disables it. In-memory, like the runtime figures
	// it is computed from.
	contextWarnPercent int

	// restartMu guards a pending request to continue the session on a new Duo
	// binary. It is in-memory on purpose: a restart that never happened is just a
	// resumable session, whereas a persisted flag would restart forever.
	restartMu      sync.RWMutex
	restartReason  string
	restartInstall bool

	// Durable session state. EnableDurability must be called once, before the
	// event loop starts, after any resume has restored the project state.
	durable   snapshotStore
	journal   *sessionstore.EventLog
	log       *sessionstore.Logger
	compose   func(project.Snapshot) sessionstore.Snapshot
	persistMu sync.Mutex

	resumeWorkMu        sync.Mutex
	resumeWorkEnabled   bool
	resumeWorkAttempted map[protocol.AgentID]bool

	holdMu             sync.Mutex
	harnessOff         bool
	resumePending      bool
	resumeWorkInFlight bool
	pauseMu            sync.Mutex
	pauseEmitMu        sync.Mutex
	driverSwitchMu     sync.Mutex
	pauseState         string
	paused             map[protocol.AgentID]bool
	settled            map[protocol.AgentID]bool
	held               []heldMessage
	pauseChange        func(PauseSnapshot)
	resumeRepo         string
	resumeSession      string

	modelMu         sync.RWMutex
	currentThinking map[protocol.AgentID]string
	currentModel    map[protocol.AgentID]string

	// injectedMu guards injected, the last prompt text Duo pushed into each
	// agent's session. See handleAssistant for why it is kept.
	injectedMu sync.Mutex
	injected   map[protocol.AgentID]string

	// summaryMu guards summaryRequested, the single final-summary request one
	// finished round may make. See requestFinalSummary.
	summaryMu        sync.Mutex
	summaryRequested bool

	// thinkingObserver records the level an agent actually reports, after any
	// live clamping. It is installed once before the event loop starts.
	thinkingObserver func(protocol.AgentID, string)
}

type heldMessage struct {
	agent   protocol.AgentID
	message protocol.Message
}

type PauseSnapshot struct {
	State  string
	Paused map[protocol.AgentID]bool
}

// Durability wires a session store into the coordinator.
type Durability struct {
	Store   snapshotStore
	Events  *sessionstore.EventLog
	Log     *sessionstore.Logger
	Compose func(project.Snapshot) sessionstore.Snapshot
}

type snapshotStore interface {
	Save(sessionstore.Snapshot) error
}

func New(
	server *transport.Server,
	state *project.State,
	tracker *harness.Tracker,
	ws workspace.Manager,
	bus *events.Bus,
) *Coordinator {
	return &Coordinator{
		server: server, project: state, tracker: tracker, workspace: ws, bus: bus,
		pauseState: "running", paused: map[protocol.AgentID]bool{protocol.Austin: false, protocol.Tony: false},
		settled: map[protocol.AgentID]bool{protocol.Austin: true, protocol.Tony: true},
	}
}

// SetAgents connects the agent manager to the coordinator, allowing it to route
// prompts, nudges, and steers to non-TCP drivers (e.g. agy running in PTY).
func (c *Coordinator) SetAgents(agents *agentpkg.Manager) {
	c.agents = agents
}

// SetLanguage selects the language for Duo-authored agent and human messages.
func (c *Coordinator) SetLanguage(language string) { c.language = language }

// SetThinkingObserver is called with the level an agent actually reports. Live
// drivers answer cycle_thinking asynchronously, so the session cannot read the
// new value from CycleThinking's return; it persists the confirmed value here.
func (c *Coordinator) SetThinkingObserver(observer func(protocol.AgentID, string)) {
	c.thinkingObserver = observer
}

// RestartRequested reports the pending request to continue on a new Duo binary,
// which the frontend reads to stop the session and exit with the upgrade code.
func (c *Coordinator) RestartRequested() (reason string, install bool) {
	c.restartMu.RLock()
	defer c.restartMu.RUnlock()
	return c.restartReason, c.restartInstall
}

// RequestRestart records that an agent asked Duo to continue on a new binary. It
// takes the reason and whether the launcher should install first.
func (c *Coordinator) RequestRestart(reason string, install bool) {
	c.restartMu.Lock()
	c.restartReason = strings.TrimSpace(reason)
	c.restartInstall = install
	c.restartMu.Unlock()
}

// HarnessHeld reports whether the harness must stay quiet. A quiet mode switch
// holds automatic nudges until the human submits a task or explicitly resumes.
func (c *Coordinator) HarnessHeld() bool {
	c.holdMu.Lock()
	off := c.harnessOff
	c.holdMu.Unlock()
	c.pauseMu.Lock()
	paused := c.pauseState != "running"
	c.pauseMu.Unlock()
	return off || paused
}

func (c *Coordinator) SetPauseObserver(observer func(PauseSnapshot)) { c.pauseChange = observer }

func (c *Coordinator) SetResumeContext(repo, session string) {
	c.resumeRepo, c.resumeSession = repo, session
}

func (c *Coordinator) RestorePause(snapshot PauseSnapshot) {
	c.pauseMu.Lock()
	if snapshot.State == "pausing" || snapshot.State == "paused" {
		c.pauseState = snapshot.State
		for _, id := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
			if snapshot.State == "paused" {
				c.paused[id] = snapshot.Paused[id]
			} else {
				c.paused[id] = c.isIdleLocked(id)
			}
		}
		if c.pauseState == "pausing" && c.allPausedLocked() {
			c.pauseState = "paused"
		}
	}
	state := c.pauseSnapshotLocked()
	c.pauseMu.Unlock()
	if state.State != "running" {
		c.emitPause(state, "pause restored")
	}
}

func (c *Coordinator) PauseSnapshot() PauseSnapshot {
	c.pauseMu.Lock()
	defer c.pauseMu.Unlock()
	return c.pauseSnapshotLocked()
}

func (c *Coordinator) NotifyPauseUpdate(text string) {
	c.emitPause(c.PauseSnapshot(), text)
}

// BeginDriverSwitch serializes driver replacements and checks that the selected
// agent is still idle. It deliberately does not hold pauseMu across replacement:
// replacing a process synchronously reports lifecycle events back to the coordinator.
func (c *Coordinator) BeginDriverSwitch(agent protocol.AgentID) (func(), error) {
	c.driverSwitchMu.Lock()
	c.pauseMu.Lock()
	projectState := c.project.Snapshot()
	canSwitch := c.pauseState == "paused" || !projectState.Started || projectState.Phase == project.PhaseDone
	if !canSwitch {
		c.pauseMu.Unlock()
		c.driverSwitchMu.Unlock()
		return nil, fmt.Errorf("driver can only be changed before the first task, while session is paused, or after DONE")
	}
	if c.tracker != nil && c.tracker.Snapshot(agent).Busy {
		c.pauseMu.Unlock()
		c.driverSwitchMu.Unlock()
		return nil, fmt.Errorf("cannot change %s driver while it is busy", agent)
	}
	c.pauseMu.Unlock()
	return c.driverSwitchMu.Unlock, nil
}

func (c *Coordinator) pauseSnapshotLocked() PauseSnapshot {
	paused := map[protocol.AgentID]bool{protocol.Austin: c.paused[protocol.Austin], protocol.Tony: c.paused[protocol.Tony]}
	return PauseSnapshot{State: c.pauseState, Paused: paused}
}

func (c *Coordinator) allPausedLocked() bool {
	return c.paused[protocol.Austin] && c.paused[protocol.Tony]
}

func (c *Coordinator) emitPause(snapshot PauseSnapshot, text string) {
	c.pauseEmitMu.Lock()
	defer c.pauseEmitMu.Unlock()
	// Activity events may race after each caller releases pauseMu. Re-read under
	// the emission lock so persistence cannot let an older projection win last.
	snapshot = c.PauseSnapshot()
	if c.pauseChange != nil {
		c.pauseChange(snapshot)
	}
	if c.bus != nil {
		c.bus.Emit(events.Event{Kind: events.KindSystem, Agent: protocol.Duo, Text: "pause: " + text})
	}
}

func (c *Coordinator) SetPause() error {
	c.pauseMu.Lock()
	if c.pauseState != "running" {
		c.pauseMu.Unlock()
		return fmt.Errorf("pause is already %s", c.pauseState)
	}
	c.pauseState = "pausing"
	for _, id := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		c.paused[id] = c.isIdleLocked(id)
	}
	if c.allPausedLocked() {
		c.pauseState = "paused"
	}
	state := c.pauseSnapshotLocked()
	c.pauseMu.Unlock()
	text := "pause requested"
	if state.State == "paused" {
		text = "both agents paused"
	}
	c.emitPause(state, text)
	return nil
}

func (c *Coordinator) CancelPause() error {
	c.pauseMu.Lock()
	if c.pauseState != "pausing" {
		c.pauseMu.Unlock()
		return fmt.Errorf("session is %s; use /continue after pause completes", c.pauseState)
	}
	c.pauseState = "running"
	c.paused[protocol.Austin], c.paused[protocol.Tony] = false, false
	held := c.held
	c.held = nil
	state := c.pauseSnapshotLocked()
	c.pauseMu.Unlock()
	c.emitPause(state, "pause cancelled")
	for _, item := range held {
		_ = c.sendToAgent(context.Background(), item.agent, item.message)
	}
	return nil
}

func (c *Coordinator) ContinuePause(ctx context.Context) error {
	c.pauseMu.Lock()
	if c.pauseState != "paused" {
		c.pauseMu.Unlock()
		return fmt.Errorf("session is %s; continue requires both agents paused", c.pauseState)
	}
	held := c.held
	c.held = nil
	c.pauseState = "running"
	c.paused[protocol.Austin], c.paused[protocol.Tony] = false, false
	state := c.pauseSnapshotLocked()
	c.pauseMu.Unlock()
	c.clearResumePending()
	c.emitPause(state, "continued")
	var summaries [2][]string
	for _, item := range held {
		index := 0
		if item.agent == protocol.Tony {
			index = 1
		}
		if item.message.Text != "" {
			summaries[index] = append(summaries[index], string(item.message.Type)+": "+strings.TrimSpace(item.message.Text))
		}
	}
	for _, id := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		// A mode change requested while pausing was held with its user-facing
		// notice. Refresh adapter mode silently before the one combined wake.
		c.sendModeChange(ctx, c.project.Mode(), "", id)
		text := c.ResumePrompt(id)
		index := 0
		if id == protocol.Tony {
			index = 1
		}
		if c.resumeRepo != "" && c.resumeSession != "" {
			logs := filepath.Join(c.resumeRepo, ".duo", "logs")
			text += fmt.Sprintf("\n\nSession transcripts (read before continuing): %s and %s. If your driver changed while paused, the previous conversation did not carry over; read your transcript first.", filepath.Join(logs, c.resumeSession+".md"), filepath.Join(logs, c.resumeSession+"-"+strings.ToLower(string(id))+".md"))
		}
		if len(summaries[index]) > 0 {
			text += "\n\nDuo held these while paused:\n- " + strings.Join(summaries[index], "\n- ")
		}
		if err := c.sendToAgent(ctx, id, protocol.Message{Version: protocol.Version, Type: protocol.MsgResumePrompt, From: protocol.Duo, To: id, Text: text, Timestamp: time.Now().UnixMilli()}); err != nil {
			return err
		}
	}
	return nil
}

func (c *Coordinator) isIdleLocked(id protocol.AgentID) bool {
	if !c.IsAgentConnected(id) {
		return true
	}
	if c.tracker == nil {
		return true
	}
	return !c.tracker.Snapshot(id).Busy && c.settled[id]
}

func (c *Coordinator) RefreshPause(agent protocol.AgentID) { c.refreshPause(agent, "") }

func (c *Coordinator) refreshPause(agent protocol.AgentID, activity protocol.ActivityType) {
	c.pauseMu.Lock()
	if activity == protocol.ActivityAgentStart {
		c.settled[agent] = false
	}
	if activity == protocol.ActivityAgentSettled {
		c.settled[agent] = true
	}
	if activity != protocol.ActivityAgentStart && activity != protocol.ActivityAgentSettled && c.tracker != nil && c.tracker.Snapshot(agent).Busy {
		c.settled[agent] = false
	}
	changed := false
	if c.pauseState != "running" {
		wasPaused := c.paused[agent]
		c.paused[agent] = c.isIdleLocked(agent)
		changed = wasPaused != c.paused[agent]
		if c.paused[agent] && c.pauseState == "pausing" && c.allPausedLocked() {
			c.pauseState = "paused"
			changed = true
		}
		if !c.paused[agent] && c.pauseState == "paused" {
			c.pauseState = "pausing"
			changed = true
		}
	}
	state := c.pauseSnapshotLocked()
	c.pauseMu.Unlock()
	if changed {
		text := "agent activity changed"
		if state.State == "paused" {
			text = "both agents paused"
		}
		if state.State == "pausing" {
			text = string(agent) + " active; waiting to settle"
		}
		c.emitPause(state, text)
	}
}

func (c *Coordinator) holdHarness() {
	c.holdMu.Lock()
	c.harnessOff = true
	c.holdMu.Unlock()
}

// HoldResume keeps a restored, already-started project quiet until a human
// explicitly resumes work or submits a new task.
func (c *Coordinator) HoldResume() {
	c.holdMu.Lock()
	c.resumePending = true
	c.harnessOff = true
	c.holdMu.Unlock()
}

// ResumePending reports whether a human has yet to explicitly resume a restored
// session.
func (c *Coordinator) ResumePending() bool {
	c.holdMu.Lock()
	defer c.holdMu.Unlock()
	return c.resumePending
}

func (c *Coordinator) clearResumePending() {
	c.holdMu.Lock()
	c.resumePending = false
	c.resumeWorkInFlight = false
	c.harnessOff = false
	c.holdMu.Unlock()
}

func (c *Coordinator) releaseHarness() {
	c.holdMu.Lock()
	c.harnessOff = false
	c.holdMu.Unlock()
}

// IsAgentConnected reports whether an agent is connected via the bridge server
// or running under the agent manager.
func (c *Coordinator) IsAgentConnected(agent protocol.AgentID) bool {
	if c.server != nil && c.server.IsConnected(agent) {
		return true
	}
	if c.agents != nil {
		if d, ok := c.agents.Driver(agent); ok && d != nil && d.Running() {
			return true
		}
	}
	return false
}

func (c *Coordinator) sendToAgent(ctx context.Context, agent protocol.AgentID, message protocol.Message) error {
	// Core wrote these, so Core is answerable for the tools they name: a prompt may
	// not instruct an agent to call a tool its driver never exposed. A human prompt
	// and a peer steer are left alone — Core does not own that wording, and deleting
	// half of what a person typed would be a lie about what they said.
	switch message.Type {
	case protocol.MsgDuoNotice, protocol.MsgHarnessPrompt, protocol.MsgResumePrompt, protocol.MsgModeChange:
		message.Text = c.filterToolMentions(agent, message.Text)
	}
	c.pauseMu.Lock()
	if c.pauseState != "running" {
		switch message.Type {
		case protocol.MsgHumanPrompt, protocol.MsgSteer, protocol.MsgDuoNotice, protocol.MsgHarnessPrompt:
			c.held = append(c.held, heldMessage{agent: agent, message: message})
			c.pauseMu.Unlock()
			return nil
		case protocol.MsgModeChange:
			if message.Text != "" {
				c.held = append(c.held, heldMessage{agent: agent, message: message})
				c.pauseMu.Unlock()
				return nil
			}
		case protocol.MsgResumePrompt:
			c.pauseMu.Unlock()
			return nil
		}
	}
	c.pauseMu.Unlock()
	// Anything that arrives in the agent's session as a user turn is recorded, so
	// handleAssistant can tell an injected prompt apart from something the agent
	// actually said. Recording here rather than at each call site means a new
	// prompt type cannot forget to do it.
	switch message.Type {
	case protocol.MsgHumanPrompt, protocol.MsgHarnessPrompt, protocol.MsgResumePrompt,
		protocol.MsgDuoNotice, protocol.MsgSteer, protocol.MsgModeChange:
		c.noteInjected(agent, message.Text)
	}
	// The bridge may only carry a request whose capability the driver declared.
	// An endpoint that observes the agent rather than receiving from Core (a
	// plugin-owned bridge) cannot deliver an injected prompt, so writing to it
	// would drop the message silently; the terminal is the delivery path there,
	// regardless of connection state.
	if c.server != nil && c.server.IsConnected(agent) && c.capabilities(agent).LiveSteering {
		return c.server.Send(ctx, agent, message)
	}
	if c.agents != nil {
		if d, ok := c.agents.Driver(agent); ok && d != nil && d.Running() {
			// The terminal is a delivery path, not a guaranteed one. A driver that
			// declares ptyFallback false is saying that nothing typed here reaches
			// it, and writing anyway is the silent drop this gate exists to prevent:
			// the prompt looks delivered and is gone. Say so instead.
			if !c.capabilities(agent).PTYFallback {
				return fmt.Errorf("%s has no delivery path: its driver declares liveSteering=false and ptyFallback=false, so a prompt cannot reach it", agent)
			}
			if message.Text != "" {
				text := message.Text
				if strings.Contains(text, "\n") {
					return d.Write([]byte("\x1b[200~" + text + "\x1b[201~\r"))
				}
				return d.Write([]byte(text + "\r"))
			}
			return nil
		}
	}
	if c.server != nil {
		return c.server.Send(ctx, agent, message)
	}
	return fmt.Errorf("%s is not connected", agent)
}

// SendToAgent delivers a message over whichever delivery path the agent's driver
// declares, and records it as an injected prompt when it is one.
//
// It is the coordinator's own sendToAgent under the name the harness and the
// session compose through: a driver whose bridge is lazy — Codex only starts its
// MCP bridge the first time it calls a Duo tool — has no client on the socket
// server at all, so handing a component the server makes that agent permanently
// unreachable to it.
func (c *Coordinator) SendToAgent(ctx context.Context, agent protocol.AgentID, message protocol.Message) error {
	return c.sendToAgent(ctx, agent, message)
}

// EnableDurability attaches persistent session state and an event journal. It
// installs the project persistence hook, so it must be called after a resume
// has finished restoring state; otherwise recovery would immediately write back.
func (c *Coordinator) EnableDurability(d Durability) {
	c.durable = d.Store
	c.journal = d.Events
	c.log = d.Log
	c.compose = d.Compose
	c.project.SetPersistence(c.persist)
}

// RequestResumeWork sends one phase-aware resume prompt to each agent. Agents
// that are not reachable yet receive it on their first bridge connection.
func (c *Coordinator) RequestResumeWork(ctx context.Context) error {
	snap := c.project.Snapshot()
	if snap.Phase == project.PhaseDone {
		return errors.New("session is DONE; submit a new task to start another round")
	}
	if !snap.Started {
		return errors.New("no task has started; submit a task first")
	}
	c.pauseMu.Lock()
	paused := c.pauseState != "running"
	c.pauseMu.Unlock()
	if paused {
		return errors.New("session is paused; use /continue")
	}
	c.holdMu.Lock()
	if !c.resumePending {
		c.holdMu.Unlock()
		return errors.New("session is already resumed")
	}
	if c.resumeWorkInFlight {
		c.holdMu.Unlock()
		return errors.New("resume is already in progress")
	}
	c.resumeWorkInFlight = true
	c.holdMu.Unlock()

	c.resumeWorkMu.Lock()
	if !c.resumeWorkEnabled {
		c.resumeWorkEnabled = true
		c.resumeWorkAttempted = make(map[protocol.AgentID]bool)
	}
	c.resumeWorkMu.Unlock()
	var firstErr error
	for _, id := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		if !c.IsAgentConnected(id) {
			continue // onConnect delivers this agent's one prompt later
		}
		if err := c.deliverResumeWork(ctx, id); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	c.holdMu.Lock()
	c.resumeWorkInFlight = false
	if firstErr == nil {
		c.resumePending = false
		c.harnessOff = false
	}
	c.holdMu.Unlock()
	return firstErr
}

func (c *Coordinator) deliverResumeWork(ctx context.Context, agent protocol.AgentID) error {
	c.resumeWorkMu.Lock()
	if !c.resumeWorkEnabled || c.resumeWorkAttempted[agent] {
		c.resumeWorkMu.Unlock()
		return nil
	}
	c.resumeWorkAttempted[agent] = true
	c.resumeWorkMu.Unlock()
	message := protocol.Message{Version: protocol.Version, Type: protocol.MsgResumePrompt, From: protocol.Duo, To: agent, Text: c.ResumePrompt(agent), Timestamp: time.Now().UnixMilli()}
	if err := c.sendToAgent(ctx, agent, message); err != nil {
		c.resumeWorkMu.Lock()
		delete(c.resumeWorkAttempted, agent)
		c.resumeWorkMu.Unlock()
		return err
	}
	if c.tracker != nil {
		c.tracker.Touch(agent)
	}
	snap := c.project.Snapshot()
	c.recordEvent("resume_work_sent", map[string]any{"agent": string(agent), "phase": string(snap.Phase), "planVersion": snap.PlanVersion})
	c.logf("sent resume work prompt to %s (phase=%s planVersion=%d)", agent, snap.Phase, snap.PlanVersion)
	c.emit(events.KindSystem, protocol.Duo, agent, fmt.Sprintf("sent resume prompt to %s", agent))
	return nil
}

// SetIntegration records the integration outcome so it is part of every
// snapshot. Recovery uses it to know whether a merge already happened.
func (c *Coordinator) SetIntegration(result workspace.IntegrationResult) {
	c.integrationMu.Lock()
	c.integration = result
	c.integrationMu.Unlock()
}

// CurrentIntegration is the last known integration outcome.
func (c *Coordinator) CurrentIntegration() workspace.IntegrationResult {
	c.integrationMu.RLock()
	defer c.integrationMu.RUnlock()
	return c.integration
}

// SetDelivery records the durable delivery checkpoint so it becomes part of
// every snapshot. It is set before the original repository is touched, so a
// crash can be reconciled idempotently on resume.
func (c *Coordinator) SetDelivery(d sessionstore.Delivery) {
	c.deliveryMu.Lock()
	// An applied checkpoint for the same final artifact is monotonic. An older
	// concurrent transaction must never turn it back into pending.
	if c.delivery.Applied() && c.delivery.FinalHead == d.FinalHead && !d.Applied() {
		c.deliveryMu.Unlock()
		return
	}
	c.delivery = d
	c.deliveryMu.Unlock()
}

// CurrentDelivery is the last known delivery checkpoint.
func (c *Coordinator) CurrentDelivery() sessionstore.Delivery {
	c.deliveryMu.RLock()
	defer c.deliveryMu.RUnlock()
	return c.delivery
}

// SetTestCommand configures an automated deterministic test command (e.g. `go test ./...`)
// that must pass before verification can be accepted.
func (c *Coordinator) SetTestCommand(cmd string) {
	c.testCommandMu.Lock()
	c.testCommand = strings.TrimSpace(cmd)
	c.testCommandMu.Unlock()
}

// TestCommand returns the currently configured test command, if any.
func (c *Coordinator) TestCommand() string {
	c.testCommandMu.RLock()
	defer c.testCommandMu.RUnlock()
	return c.testCommand
}

// runTestGate executes the test command in Austin's working directory.
func (c *Coordinator) runTestGate(ctx context.Context) error {
	cmdStr := c.TestCommand()
	if cmdStr == "" || c.workspace == nil {
		return nil
	}
	set := c.workspace.Set()
	dir := set.Austin.Path
	if scopedDir, ok, err := set.AgentDir(protocol.Austin); err == nil && ok && scopedDir != "" {
		dir = scopedDir
	}
	if dir == "" {
		return nil
	}

	testCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(testCtx, "cmd", "/C", cmdStr)
	} else {
		cmd = exec.CommandContext(testCtx, "sh", "-c", cmdStr)
	}
	cmd.Dir = dir
	cmd.Env = os.Environ()

	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return err
		}
		if len(msg) > 1500 {
			msg = msg[len(msg)-1500:]
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

// persist writes the current snapshot. It ignores the snapshot passed by the
// hook and re-reads live state under a mutex, so concurrent Austin/Tony message
// handlers can never write an older snapshot over a newer one.
func (c *Coordinator) persist(_ project.Snapshot) {
	if c.durable == nil || c.compose == nil {
		return
	}
	c.persistMu.Lock()
	defer c.persistMu.Unlock()
	if err := c.durable.Save(c.compose(c.project.Snapshot())); err != nil {
		if c.log != nil {
			c.log.Printf("failed to persist session state: %v", err)
		}
		c.emit(events.KindError, protocol.Duo, "", "failed to persist session state: "+err.Error())
	}
}

// persistNow forces a snapshot write for state that lives outside
// project.State, such as the integration result.
func (c *Coordinator) persistNow(reason string) {
	if err := c.persistStrict(reason); err != nil {
		if c.log != nil {
			c.log.Printf("failed to persist session state after %s: %v", reason, err)
		}
		c.emit(events.KindError, protocol.Duo, "", "failed to persist session state: "+err.Error())
	}
}

// persistStrict is for delivery checkpoints. Callers must stop before Git when
// it fails, because an unrecorded handoff cannot be safely recovered.
func (c *Coordinator) persistStrict(reason string) error {
	if c.durable == nil || c.compose == nil {
		return nil
	}
	c.persistMu.Lock()
	defer c.persistMu.Unlock()
	if err := c.durable.Save(c.compose(c.project.Snapshot())); err != nil {
		return fmt.Errorf("persist %s: %w", reason, err)
	}
	return nil
}

func (c *Coordinator) recordEvent(eventType string, fields map[string]any) {
	if c.journal == nil {
		return
	}
	c.journal.Record(eventType, fields)
}

func (c *Coordinator) logf(format string, args ...any) {
	if c.log != nil {
		c.log.Printf(format, args...)
	}
}

func (c *Coordinator) emit(kind events.Kind, agent, peer protocol.AgentID, text string) {
	if c.bus == nil {
		return
	}
	c.bus.Emit(events.Event{Kind: kind, Agent: agent, Peer: peer, Text: text})
}

// emitConnection publishes the system line a bridge attach or detach produces and
// marks it as a connection event. The text is the same one the timeline has always
// shown; the mark is what lets a frontend that attached after this event recognise
// it as the connection it already announced from the session view.
func (c *Coordinator) emitConnection(agent protocol.AgentID, state events.ConnectionEvent, text string) {
	if c.bus == nil {
		return
	}
	c.bus.Emit(events.Event{Kind: events.KindSystem, Agent: agent, Text: text, Connection: state})
}

// SubmitUserTask is the single human entry point used by the Duo TUI. Goal and
// Step tasks are sent to both agents; Fast tasks go only to Austin. On a DONE
// session it first reopens the round, so a composer follow-up is a normal task.
func (c *Coordinator) SubmitUserTask(ctx context.Context, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	c.pauseMu.Lock()
	paused := c.pauseState != "running"
	c.pauseMu.Unlock()
	if paused {
		return errors.New("session is pausing or paused; use /continue after pause completes")
	}

	if strings.HasPrefix(text, "/mode") {
		fields := strings.Fields(text)
		if len(fields) != 2 {
			err := fmt.Errorf("usage: /mode [fast|goal|step]")
			c.emit(events.KindSystem, protocol.Duo, "", err.Error())
			return err
		}
		target, err := project.ParseMode(fields[1])
		if err != nil {
			c.emit(events.KindSystem, protocol.Duo, "", err.Error())
			return err
		}
		return c.SwitchMode(ctx, target, "")
	}
	if strings.HasPrefix(text, "/escalate") {
		reason := strings.TrimSpace(strings.TrimPrefix(text, "/escalate"))
		if strings.HasPrefix(reason, ":") {
			reason = strings.TrimSpace(strings.TrimPrefix(reason, ":"))
		}
		return c.SwitchMode(ctx, project.ModeGoal, reason)
	}

	c.clearResumePending()
	if !c.IsAgentConnected(protocol.Austin) {
		return fmt.Errorf("Austin is not connected yet")
	}
	mode := c.project.Snapshot().EffectiveMode()
	collaborative := mode != project.ModeFast
	if collaborative && !c.IsAgentConnected(protocol.Tony) {
		return fmt.Errorf("Tony is not connected; %s mode requires both Austin and Tony", mode.Display())
	}
	c.reopenFinishedSession()
	c.project.MarkStarted()
	c.tracker.Touch(protocol.Austin)
	promptText := "[Human task from Duo]\n\n" + text
	if c.language == "zh" {
		promptText = "[Human task from Duo]\n\n以下是用户提交的原始任务，请按原文执行：\n\n" + text
	}
	if collaborative {
		for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
			c.tracker.Touch(agent)
			c.emit(events.KindUser, protocol.Duo, agent, text)
			prompt := goalTaskPrompt(agent, text, c.language)
			if mode == project.ModeStep {
				prompt = stepTaskPrompt(agent, text, c.language)
			}
			if err := c.sendToAgent(ctx, agent, protocol.Message{
				Version: 1, Type: protocol.MsgHumanPrompt, From: protocol.Duo, To: agent,
				Text: prompt, Timestamp: time.Now().UnixMilli(),
			}); err != nil {
				return fmt.Errorf("send %s task to %s: %w", mode.Display(), agent, err)
			}
		}
		c.acknowledgeTask(mode)
		return nil
	}
	c.emit(events.KindUser, protocol.Duo, protocol.Austin, text)
	guidance := "(Duo guidance: Once your work is committed and your worktree is clean, request verification with duo_set_status ready=true so Tony can independently verify.)"
	if c.language == "zh" {
		guidance = "（Duo 提示：工作提交完成且 worktree 干净后，调用 duo_set_status ready=true 请求 Tony 独立验证。）"
	}
	promptText += "\n\n" + guidance
	if err := c.sendToAgent(ctx, protocol.Austin, protocol.Message{
		Version: 1, Type: protocol.MsgHumanPrompt, From: protocol.Duo, To: protocol.Austin,
		Text: promptText, Timestamp: time.Now().UnixMilli(),
	}); err != nil {
		return err
	}
	c.acknowledgeTask(mode)
	return nil
}

// acknowledgeTask closes the gap between "duo accepted the task" and an agent's
// first token. Model latency is outside Duo's control, but the harness can always
// tell the human immediately that the task was accepted and who is working on it.
func (c *Coordinator) acknowledgeTask(mode project.Mode) {
	text := "Task received. Austin is working on it."
	if mode != project.ModeFast {
		text = fmt.Sprintf("Task received. Austin and Tony are working on it in %s mode.", mode.Display())
	}
	if c.language == "zh" {
		text = "任务已收到。Austin 正在处理。"
		if mode != project.ModeFast {
			text = fmt.Sprintf("任务已收到。Austin 和 Tony 正在按 %s 模式协作处理。", mode.Display())
		}
	}
	c.emit(events.KindHarness, protocol.Duo, "", text)
}

func stepTaskPrompt(agent protocol.AgentID, task, language string) string {
	if language == "zh" {
		role := "你是 Austin，STEP 模式的协作成员。"
		if agent == protocol.Tony {
			role = "你是 Tony，STEP 模式的独立共同设计者和验证者。"
		}
		return fmt.Sprintf("[Duo Step task: PLAN]\n%s\n\nDuo 已把任务同时发送给双方。当前阶段是 PLAN：先调用 duo_status 查看权威状态。双方共同把任务拆成有序队列，用 duo_set_plan 写计划说明、用 duo_set_steps 提交 Markdown 复选框队列；每个条目标记 [fast] 或 [goal]（未标记默认 fast）。FAST 步骤由 Austin 实现、Tony 用 duo_set_verification 验证；GOAL 步骤由双方共同实现、交叉审查并签署 EXECUTE/REVIEW/INTEGRATE。最后一步始终需要双方签署完成队列。队列商定后，双方调用 duo_set_status ready=true 批准并进入第一步。\n\n用户原始任务：\n%s", role, task)
	}
	role := "You are Austin, a collaborating agent in STEP mode."
	if agent == protocol.Tony {
		role = "You are Tony, an independent co-designer and reviewer in STEP mode."
	}
	return fmt.Sprintf("[Duo Step task: PLAN]\n%s\n\nDuo sent the task to both of you. The current phase is PLAN: call duo_status first to inspect authoritative state. Together, split the task into an ordered queue: use duo_set_plan for the plan prose and duo_set_steps for a Markdown checkbox queue. Tag each item [fast] or [goal]; an untagged item defaults to FAST. A FAST item is Austin implementing and Tony verifying with duo_set_verification. A GOAL item is both agents implementing, cross-reviewing and signing EXECUTE/REVIEW/INTEGRATE. The final step always needs both agents' signatures. Once aligned, both call duo_set_status ready=true to approve the queue and start step 1.\n\nOriginal human task:\n%s", role, task)
}

func goalTaskPrompt(agent protocol.AgentID, task, language string) string {
	if language == "zh" {
		role := "你是 Austin，Goal 模式的协作成员。"
		if agent == protocol.Austin {
			role += "请先独立理解任务，并与 Tony 协商；不要等待 Tony 被你唤醒，因为 Duo 已同时向双方发送任务。"
		} else {
			role = "你是 Tony，Goal 模式的独立共同设计者。请立即开始，不要等待 Austin 再发消息；先形成自己的方案，再通过 duo_send 与 Austin 讨论。"
		}
		return fmt.Sprintf("[Duo Goal task: PLAN]\n%s\n\n当前阶段是 PLAN：先调用 duo_status 查看权威状态；双方共同使用 duo_send 讨论方案，并用 duo_set_plan 维护完整共享计划。方案达成一致后，各自调用 duo_set_status ready=true 批准当前计划版本。Duo Core 收到双方批准后才会推进到 EXECUTE。\n\n用户原始任务：\n%s", role, task)
	}
	role := "You are Austin, a collaborating agent in Goal mode. Independently understand the task and coordinate with Tony; Duo has already sent the task to both of you, so do not wait for Tony to be woken by you."
	if agent == protocol.Tony {
		role = "You are Tony, an independent co-designer in Goal mode. Start now without waiting for another message from Austin: form your own proposal, then discuss it with Austin using duo_send."
	}
	return fmt.Sprintf("[Duo Goal task: PLAN]\n%s\n\nThe current phase is PLAN. First call duo_status to inspect authoritative state. Discuss the proposal with duo_send and maintain the complete shared plan with duo_set_plan. Once aligned, each of you must call duo_set_status ready=true to approve the current plan version. Duo Core advances to EXECUTE only after both approvals.\n\nOriginal human task:\n%s", role, task)
}

// EscalateToGoal dynamically escalates an active Fast mode session to Goal mode.
// It switches mode to Goal and phase to PLAN, creates a shared plan v1 seeded
// with the escalation reason, invalidates approvals, persists the updated state,
// and notifies both agents of their updated roles.
func (c *Coordinator) EscalateToGoal(ctx context.Context, reason string) error {
	return c.escalateToGoal(ctx, reason, true)
}

func (c *Coordinator) escalateToGoal(ctx context.Context, reason string, notify bool) error {
	reason = strings.TrimSpace(reason)
	if _, err := c.project.EscalateToGoal(reason); err != nil {
		return err
	}

	c.recordEvent("mode_escalated", map[string]any{
		"from":   project.ModeFast.String(),
		"to":     project.ModeGoal.String(),
		"reason": reason,
	})

	msg := "mode escalated: FAST → GOAL"
	if reason != "" {
		msg += fmt.Sprintf(" (reason: %s)", reason)
	}
	c.emit(events.KindSystem, protocol.Duo, "", msg)

	_ = c.persistStrict("mode escalated")
	if c.agents != nil {
		c.agents.SetMode(project.ModeGoal.String())
	}
	if c.tracker != nil {
		c.tracker.Touch(protocol.Austin)
		c.tracker.Touch(protocol.Tony)
	}
	if !notify {
		c.holdHarness()
		// Update the adapters' prompt mode without injecting a user turn.
		c.sendModeChange(ctx, project.ModeGoal, "")
		return nil
	}
	c.clearResumePending()

	reasonDesc := "(none provided)"
	if reason != "" {
		reasonDesc = reason
	}

	austinNotice := fmt.Sprintf(
		"[Duo Mode Escalation]\nThe session has escalated from Fast mode to Goal mode.\nReason: %s\nPhase: PLAN (shared plan v1 initialized).\n\nFast-mode single-driver workflow has switched to Goal-mode collaborative workflow:\n- Both Austin and Tony must co-design the shared plan.\n- Refine the shared plan using duo_set_plan.\n- When aligned, both Austin and Tony must approve with duo_set_status(ready=true) to advance to EXECUTE.\n- Existing commits in your worktree are preserved.",
		reasonDesc,
	)
	if c.language == "zh" {
		austinNotice = fmt.Sprintf("[Duo Mode Escalation]\n会话已从 Fast 模式切换到 Goal 模式。\n原因：%s\n阶段：PLAN（已初始化共享计划 v1）。\n\n工作流已切换为 Goal 协作模式：Austin 和 Tony 共同设计共享计划；使用 duo_set_plan 修改计划；双方意见一致后，都调用 duo_set_status(ready=true) 进入 EXECUTE。你 worktree 中已有的提交会保留。", reasonDesc)
	}
	tonyNotice := fmt.Sprintf(
		"[Duo Mode Escalation]\nThe session has escalated from Fast mode to Goal mode.\nReason: %s\nPhase: PLAN (shared plan v1 initialized).\n\nYou are now an active co-designer and co-developer (no longer read-only):\n- Read current plan with duo_status and collaborate with Austin via duo_send.\n- Refine the shared plan using duo_set_plan.\n- When aligned, both Austin and Tony must approve with duo_set_status(ready=true) to advance to EXECUTE.\n- In EXECUTE, work independently in your own worktree.",
		reasonDesc,
	)
	if c.language == "zh" {
		tonyNotice = fmt.Sprintf("[Duo Mode Escalation]\n会话已从 Fast 模式切换到 Goal 模式。\n原因：%s\n阶段：PLAN（已初始化共享计划 v1）。\n\n你现在是主动共同设计者和开发者（不再是只读角色）：使用 duo_status 查看计划，通过 duo_send 与 Austin 协作，使用 duo_set_plan 修改计划。双方意见一致后，都调用 duo_set_status(ready=true) 进入 EXECUTE；在 EXECUTE 阶段于自己的 worktree 独立工作。", reasonDesc)
	}

	_ = c.sendToAgent(ctx, protocol.Austin, protocol.Message{
		Version:   protocol.Version,
		Type:      protocol.MsgDuoNotice,
		From:      protocol.Duo,
		To:        protocol.Austin,
		Text:      austinNotice,
		Timestamp: time.Now().UnixMilli(),
	})
	_ = c.sendToAgent(ctx, protocol.Tony, protocol.Message{
		Version:   protocol.Version,
		Type:      protocol.MsgDuoNotice,
		From:      protocol.Duo,
		To:        protocol.Tony,
		Text:      tonyNotice,
		Timestamp: time.Now().UnixMilli(),
	})

	// The two detailed notices above carry the instructions. This bare message
	// updates the adapters' current mode so their next prompt is Goal-shaped
	// without restarting either agent.
	c.sendModeChange(ctx, project.ModeGoal, "")
	return nil
}

// SwitchMode changes the session's workflow at any time and tells both agents
// about the new mode. The domain resets the round so the new mode never inherits
// a plan, queue or signature that was produced under different gates.
func (c *Coordinator) SwitchMode(ctx context.Context, target project.Mode, reason string) error {
	return c.switchMode(ctx, target, reason, true)
}

// SwitchModeQuiet changes the workflow without injecting the mode notice into
// the agents. The interactive frontend uses it before asking whether the human
// wants the agents to resume; a mode change by itself must not start work.
func (c *Coordinator) SwitchModeQuiet(ctx context.Context, target project.Mode, reason string) error {
	return c.switchMode(ctx, target, reason, false)
}

func (c *Coordinator) switchMode(ctx context.Context, target project.Mode, reason string, notify bool) error {
	if target != project.ModeFast && target != project.ModeGoal && target != project.ModeStep {
		return fmt.Errorf("unknown Duo mode %q (expected fast, goal or step)", target)
	}
	reason = strings.TrimSpace(reason)
	before := c.project.Snapshot()
	from := before.EffectiveMode()
	if target == from {
		c.emit(events.KindSystem, protocol.Duo, "", fmt.Sprintf("already in %s mode", target.Display()))
		return nil
	}

	// Fast → Goal keeps its historical escalation behavior: seed the goal plan
	// with the reason and any prior verification note.
	if from == project.ModeFast && target == project.ModeGoal && before.Phase != project.PhaseDone {
		return c.escalateToGoal(ctx, reason, notify)
	}

	snap, err := c.project.SwitchMode(target, reason)
	if err != nil {
		return err
	}
	if snap.Phase != project.PhaseDone {
		// A pending delivery or merge belongs to the previous mode's round.
		c.SetDelivery(sessionstore.Delivery{})
		c.SetIntegration(workspace.IntegrationResult{})
	}
	c.unclaimFinalSummary()
	c.recordEvent("mode_switched", map[string]any{
		"from":   from.String(),
		"to":     target.String(),
		"reason": reason,
		"phase":  string(snap.Phase),
	})
	msg := fmt.Sprintf("mode switched: %s → %s", from.Display(), target.Display())
	if reason != "" {
		msg += fmt.Sprintf(" (reason: %s)", reason)
	}
	c.emit(events.KindSystem, protocol.Duo, "", msg)
	if err := c.persistStrict("mode switched"); err != nil {
		c.logf("persist mode switch: %v", err)
		c.emit(events.KindError, protocol.Duo, "", "mode changed in memory but could not be persisted: "+err.Error())
	}
	if c.agents != nil {
		c.agents.SetMode(target.String())
	}
	if notify {
		c.clearResumePending()
	} else {
		c.holdHarness()
	}

	if notify {
		for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
			text := c.modeSwitchNotice(agent, target, reason, snap.Phase == project.PhaseDone)
			c.sendModeChange(ctx, target, text, agent)
		}
	} else {
		// The bridge still needs the target mode to shape its next prompt; an
		// empty text updates that state without becoming a user turn.
		c.sendModeChange(ctx, target, "")
	}
	if c.tracker != nil {
		c.tracker.Touch(protocol.Austin)
		c.tracker.Touch(protocol.Tony)
	}
	return nil
}

// ResumeMode sends the current mode's work notice to both agents. It performs no
// state transition: the frontend uses it after a quiet SwitchMode, once the
// human confirms that the agents should resume now.
func (c *Coordinator) ResumeMode(ctx context.Context) error {
	c.clearResumePending()
	snap := c.project.Snapshot()
	mode := snap.EffectiveMode()
	for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		text := c.modeSwitchNotice(agent, mode, "", snap.Phase == project.PhaseDone)
		c.sendModeChange(ctx, mode, text, agent)
	}
	if c.tracker != nil {
		c.tracker.Touch(protocol.Austin)
		c.tracker.Touch(protocol.Tony)
	}
	return nil
}

// sendModeChange updates one or both adapters' current mode. The text is the
// prompt Duo injects; an empty text is used when another detailed notice already
// carries the instructions.
func (c *Coordinator) sendModeChange(ctx context.Context, mode project.Mode, text string, agents ...protocol.AgentID) {
	if len(agents) == 0 {
		agents = []protocol.AgentID{protocol.Austin, protocol.Tony}
	}
	for _, agent := range agents {
		_ = c.sendToAgent(ctx, agent, protocol.Message{
			Version:   protocol.Version,
			Type:      protocol.MsgModeChange,
			From:      protocol.Duo,
			To:        agent,
			Mode:      mode.String(),
			Text:      text,
			Timestamp: time.Now().UnixMilli(),
		})
	}
}

func (c *Coordinator) modeSwitchNotice(agent protocol.AgentID, target project.Mode, reason string, done bool) string {
	if c.language == "zh" {
		return c.modeSwitchNoticeZh(agent, target, reason, done)
	}
	var text string
	switch target {
	case project.ModeFast:
		text = "[Duo mode change: FAST]\nThe session is now in FAST mode. Austin implements; Tony independently verifies from his own worktree. There is no shared plan and no step queue. Austin: commit your work, keep the worktree clean, then call duo_set_status ready=true to request verification. Tony: do not commit or edit; report with duo_set_verification when requested."
	case project.ModeGoal:
		text = "[Duo mode change: GOAL]\nThe session is now in GOAL mode and the current round starts in PLAN. Both agents co-design the plan with duo_send and duo_set_plan, then both approve with duo_set_status ready=true. In EXECUTE both implement in their own worktrees; REVIEW cross-checks the peer's HEAD; INTEGRATE merges Tony's branch into Austin's and both sign the integrated HEAD."
	case project.ModeStep:
		text = "[Duo mode change: STEP]\nThe session is now in STEP mode and the current round starts in PLAN. Both agents agree on a queue with duo_set_steps and approve it with duo_set_status ready=true. Tag each item [fast] or [goal]. A FAST item is Austin implementing and Tony verifying with duo_set_verification. A GOAL item is both implementing, cross-reviewing and signing EXECUTE/REVIEW/INTEGRATE. Every final step also needs both agents' signatures."
	}
	if done {
		text += "\nThe current round is DONE; the new mode applies to the next task."
	}
	if reason != "" {
		text += "\nReason: " + reason
	}
	return text
}

func (c *Coordinator) modeSwitchNoticeZh(agent protocol.AgentID, target project.Mode, reason string, done bool) string {
	var text string
	switch target {
	case project.ModeFast:
		text = "[Duo 模式切换：FAST]\n会话已切换到 FAST 模式。Austin 负责实现；Tony 在自己的 worktree 中独立验证。没有共享计划和步骤队列。Austin：提交工作并保持 worktree 干净，然后调用 duo_set_status ready=true 请求验证。Tony：不要提交或编辑；收到请求后用 duo_set_verification 报告结论。"
	case project.ModeGoal:
		text = "[Duo 模式切换：GOAL]\n会话已切换到 GOAL 模式，当前轮次从 PLAN 开始。双方通过 duo_send 和 duo_set_plan 共同设计计划，然后各自调用 duo_set_status ready=true 批准。EXECUTE 阶段双方在各自 worktree 实现；REVIEW 阶段交叉审查对方的 HEAD；INTEGRATE 阶段把 Tony 的分支合并到 Austin 的分支，双方签署集成后的 HEAD。"
	case project.ModeStep:
		text = "[Duo 模式切换：STEP]\n会话已切换到 STEP 模式，当前轮次从 PLAN 开始。双方用 duo_set_steps 商定队列，并用 duo_set_status ready=true 批准。每个条目标记 [fast] 或 [goal]：FAST 条目由 Austin 实现、Tony 用 duo_set_verification 验证；GOAL 条目由双方共同实现、交叉审查并签署 EXECUTE/REVIEW/INTEGRATE。最后一步还需要双方签署完成队列。"
	}
	if done {
		text += "\n当前轮次已 DONE；新模式从下一个任务开始生效。"
	}
	if reason != "" {
		text += "\n原因：" + reason
	}
	return text
}

// reopenFinishedSession turns a DONE session back into an active round when the
// human submits a new task through the Duo composer. A native attach
// (Ctrl+A/Ctrl+T) writes straight to the agent PTY and never reaches this path,
// so Austin reopening a finished round from there is handled separately by
// reopenFastRoundForNewWork when it requests verification of a new HEAD.
//
// The finished round's delivery and integration checkpoints are cleared first:
// they are terminal evidence for the previous artifact, and an applied record
// would otherwise let deliverFinal short-circuit the next round back to DONE
// without a fresh verification.
func (c *Coordinator) reopenFinishedSession() {
	if c.project.Snapshot().Phase != project.PhaseDone {
		return
	}
	c.SetDelivery(sessionstore.Delivery{})
	c.SetIntegration(workspace.IntegrationResult{})
	snap, err := c.project.Reopen()
	if err != nil {
		c.logf("reopen finished session: %v", err)
		return
	}
	// The finished round has already reported on itself; the new one gets its
	// own summary when it is delivered.
	c.unclaimFinalSummary()
	c.persistNow("session reopened for a new task")
	c.recordEvent("round_started", map[string]any{"phase": string(snap.Phase)})
	c.logf("session reopened after DONE → %s for a new human task", snap.Phase)
	c.emit(events.KindSystem, protocol.Duo, "", fmt.Sprintf("new task after delivery: session reopened in %s", snap.Phase))
	if !c.IsAgentConnected(protocol.Tony) {
		c.emit(events.KindError, protocol.Duo, "",
			"Tony is not connected; restart it with Ctrl+Y, otherwise the new round cannot be verified")
	}
}

func (c *Coordinator) StatusText(ctx context.Context) string { return c.statusText(ctx) }

func (c *Coordinator) OnConnect(ctx context.Context, client *transport.Client) {
	c.onConnect(ctx, client.Identity())
}

// onConnect applies a bridge connection for an agent whose identity the transport
// has already authenticated. It is separate from OnConnect so the routing decision
// can be exercised without a socket, and so no caller can pass an identity the
// transport has not vouched for.
func (c *Coordinator) onConnect(ctx context.Context, agent protocol.AgentID) {
	c.tracker.Touch(agent)
	c.recordEvent("bridge_connect", map[string]any{"agent": string(agent)})
	// Drivers that cannot announce themselves over the bridge already said so when
	// their process started, so repeating it here would report one agent twice.
	if !c.announcedOnStart(agent) {
		c.emitConnection(agent, events.ConnectionUp, fmt.Sprintf("%s connected", agent))
	}
	if err := c.deliverResumeWork(ctx, agent); err != nil {
		snap := c.project.Snapshot()
		c.recordEvent("resume_work_failed", map[string]any{"agent": string(agent), "phase": string(snap.Phase), "error": err.Error()})
		c.logf("resume work for %s failed: %v", agent, err)
		c.emit(events.KindError, protocol.Duo, agent, fmt.Sprintf("resume work for %s failed: %v", agent, err))
		return
	}
}

// announcedOnStart reports whether Duo already announced this agent when its
// process started, because its driver does not announce itself over the bridge.
//
// The answer comes from the driver's declared selfReports capability. A driver whose
// bridge attaches the moment the agent starts speaks for itself; one that cannot
// attach until its agent has something to say needs Duo to say it once, or the human
// is told an agent is missing when it is already working.
func (c *Coordinator) announcedOnStart(agent protocol.AgentID) bool {
	if c.agents == nil {
		return false
	}
	d, ok := c.agents.Driver(agent)
	if !ok || d == nil {
		return false
	}
	return !d.Capabilities().SelfReports
}

func (c *Coordinator) OnDisconnect(client *transport.Client) {
	c.onDisconnect(client.Identity())
}

// onDisconnect applies a bridge detach for an agent whose identity the transport
// has already authenticated. It is separate from OnDisconnect for the same reason
// onConnect is: the routing decision can be exercised without a socket, and no
// caller can name an agent the transport has not vouched for.
func (c *Coordinator) onDisconnect(agent protocol.AgentID) {
	c.tracker.Reset(agent)
	c.recordEvent("bridge_disconnect", map[string]any{"agent": string(agent)})
	c.emitConnection(agent, events.ConnectionDown, fmt.Sprintf("%s disconnected", agent))
}

func (c *Coordinator) OnMessage(ctx context.Context, client *transport.Client, message protocol.Message) {
	switch message.Type {
	case protocol.MsgTest:
		c.emit(events.KindSystem, client.Identity(), "", "TEST: "+message.Text)

	case protocol.MsgActivity:
		c.handleActivity(client.Identity(), message)

	case protocol.MsgUsage:
		c.handleUsage(client.Identity(), message)

	case protocol.MsgAssistantMessage:
		c.handleAssistant(client.Identity(), message)

	case protocol.MsgAgentError:
		c.handleAgentError(client.Identity(), message)

	case protocol.MsgPeerMessage:
		c.handlePeerMessage(ctx, client, message)

	case protocol.MsgSetPlan:
		c.handleSetPlan(ctx, client, message)

	case protocol.MsgSetSteps:
		c.handleSetSteps(ctx, client, message)

	case protocol.MsgRestartDuo:
		c.handleRestartDuo(ctx, client, message)

	case protocol.MsgSetStatus:
		c.handleSetStatus(ctx, client, message)

	case protocol.MsgSetVerification:
		c.handleSetVerification(ctx, client, message)

	case protocol.MsgEscalate:
		c.handleEscalate(ctx, client, message)

	case protocol.MsgGetStatus:
		c.handleGetStatus(ctx, client, message)

	case protocol.MsgModelState:
		c.handleModelState(client.Identity(), message)

	case protocol.MsgThinkingState:
		c.handleThinkingState(client.Identity(), message)

	default:
		if message.RequestID != "" {
			_ = c.respond(ctx, client, message.RequestID, false, "unknown message type: "+string(message.Type), "")
		}
	}
}

func (c *Coordinator) handleActivity(agent protocol.AgentID, message protocol.Message) {
	c.tracker.Handle(agent, message.Activity)
	c.tracker.Note(agent, message.Activity, message.Tool, message.Detail)
	c.refreshPause(agent, message.Activity)
	// Each figure is gated on the capability that promises it. A driver that
	// reports context usage it cannot maintain, or a rate it computed from
	// something other than its own output, would otherwise put a number on
	// screen that the operator has no way to distrust.
	caps := c.capabilities(agent)
	if caps.ContextUsage {
		c.tracker.UpdateContext(agent, message.ContextTokens, message.ContextWindow)
		c.checkContextPressure(agent)
	}
	if caps.TokenRate {
		c.tracker.UpdateRate(agent, message.TokensPerSecond)
	}
	if message.Activity == protocol.ActivityAgentStart {
		c.project.MarkStarted()
	}
	if c.bus != nil {
		c.bus.Emit(events.Event{Kind: events.KindActivity, Agent: agent})
	}
}

// capabilities returns what the agent's driver declared. A missing driver
// yields every capability off, so the zero set is the most restricted path —
// never the most capable.
func (c *Coordinator) capabilities(agent protocol.AgentID) driver.Capabilities {
	if c.agents == nil {
		return driver.Capabilities{}
	}
	return c.agents.CapabilitiesFor(agent)
}

func (c *Coordinator) handleAssistant(agent protocol.AgentID, message protocol.Message) {
	if strings.TrimSpace(message.Text) == "" {
		return
	}
	// An agent reporting back the exact prompt Duo injected is not news; it is
	// the prompt, and a human reading the timeline cannot tell the difference.
	// A bridge that mis-attributes an injected message would otherwise fill the
	// conversation with echoes of the human's own words. This guard is
	// driver-independent on purpose: the bridge is the thing that got it wrong,
	// so the core should not depend on any one bridge getting it right.
	if c.wasInjected(agent, message.Text) {
		c.logf("%s assistant message discarded: it repeats the prompt Duo injected", agent)
		return
	}
	c.tracker.Touch(agent)
	c.emit(events.KindAssistant, agent, "", message.Text)
	// The final report is the one message Duo promised the human, and it arrives
	// after delivery, when nobody is watching the dashboard any more.
	if agent == protocol.Austin && protocol.IsSummaryReport(message.Text) {
		c.attention(events.AttentionSummary)
	}
}

// noteInjected records the prompt text sent to an agent so handleAssistant can
// recognize it coming back.
func (c *Coordinator) noteInjected(agent protocol.AgentID, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	c.injectedMu.Lock()
	defer c.injectedMu.Unlock()
	if c.injected == nil {
		c.injected = make(map[protocol.AgentID]string, 2)
	}
	c.injected[agent] = text
}

func (c *Coordinator) wasInjected(agent protocol.AgentID, text string) bool {
	c.injectedMu.Lock()
	defer c.injectedMu.Unlock()
	previous, ok := c.injected[agent]
	return ok && previous != "" && strings.TrimSpace(text) == previous
}

// SetModel asks one agent to switch its active model. It goes over the bridge
// only when the driver declared liveModelSwitch: an endpoint that observes the
// agent rather than driving it (a plugin-owned bridge) cannot reach a running
// session, so the local record-and-restart path is the one that keeps the
// selection honest. Without the gate, a connected-but-silent endpoint would
// swallow the message and the model would silently never change.
func (c *Coordinator) SetModel(ctx context.Context, agent protocol.AgentID, provider, id string) error {
	provider, id = strings.TrimSpace(provider), strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("set model: model id is required")
	}
	if c.server != nil && c.server.IsConnected(agent) && c.capabilities(agent).LiveModelSwitch {
		if provider == "" {
			if slash := strings.Index(id, "/"); slash != -1 {
				provider = id[:slash]
				id = id[slash+1:]
			} else {
				return fmt.Errorf("set model: provider and model id are required")
			}
		}
		return c.server.Send(ctx, agent, protocol.Message{
			Version: protocol.Version, Type: protocol.MsgSetModel, From: protocol.Duo, To: agent,
			Provider: provider, Model: id, Timestamp: time.Now().UnixMilli(),
		})
	}
	// For offline or non-TCP agents (e.g. agy driver), record model directly and notify listeners.
	c.handleModelState(agent, protocol.Message{
		OK:       true,
		Provider: provider,
		Model:    id,
	})
	return nil
}

func nextThinkingLevel(current string) string {
	switch strings.ToLower(strings.TrimSpace(current)) {
	case "off", "none", "":
		return "low"
	case "low":
		return "medium"
	case "medium":
		return "high"
	case "high":
		return "max"
	case "max":
		return "off"
	default:
		return "medium"
	}
}

// CycleThinking advances one agent's thinking level. The bridge carries it only
// when the driver declared liveThinkingSwitch — for the same reason as SetModel:
// an endpoint that cannot reach the running agent would drop the message and the
// picker would appear to cycle while nothing changed. Otherwise the level is
// cycled locally and applied on the next launch.
func (c *Coordinator) CycleThinking(ctx context.Context, agent protocol.AgentID) error {
	if c.server != nil && c.server.IsConnected(agent) && c.capabilities(agent).LiveThinkingSwitch {
		return c.server.Send(ctx, agent, protocol.Message{
			Version: protocol.Version, Type: protocol.MsgCycleThinking, From: protocol.Duo, To: agent,
			Timestamp: time.Now().UnixMilli(),
		})
	}

	c.modelMu.Lock()
	if c.currentThinking == nil {
		c.currentThinking = make(map[protocol.AgentID]string)
	}
	next := nextThinkingLevel(c.currentThinking[agent])
	c.currentThinking[agent] = next
	c.modelMu.Unlock()

	c.handleThinkingState(agent, protocol.Message{
		OK:       true,
		Thinking: next,
	})
	return nil
}

// handleModelState records which model an agent is running. A failed switch
// (ok=false) is surfaced as an error without counting as an agent failure.
func (c *Coordinator) handleModelState(agent protocol.AgentID, message protocol.Message) {
	provider := strings.TrimSpace(message.Provider)
	model := strings.TrimSpace(message.Model)
	if !message.OK {
		text := strings.TrimSpace(message.Text)
		if text == "" {
			text = "could not switch model"
		}
		c.emit(events.KindError, agent, "", text)
		return
	}
	if model == "" {
		return
	}
	ref := model
	// A bare-reference driver reports its id complete; only a driver that spells
	// models "provider/id" gets the reported provider joined back on.
	if provider != "" && !c.agents.ManifestFor(agent).Bare() && !strings.HasPrefix(model, provider+"/") {
		ref = provider + "/" + model
	}
	c.modelMu.Lock()
	if c.currentModel == nil {
		c.currentModel = make(map[protocol.AgentID]string)
	}
	c.currentModel[agent] = ref
	c.modelMu.Unlock()

	if c.bus != nil {
		c.bus.Emit(events.Event{Kind: events.KindModel, Agent: agent, Provider: provider, Model: model})
	}
	c.logf("%s model: %s", agent, ref)
}

// Model returns the currently active model reference for an agent.
func (c *Coordinator) Model(agent protocol.AgentID) string {
	c.modelMu.RLock()
	defer c.modelMu.RUnlock()
	if c.currentModel == nil {
		return ""
	}
	return c.currentModel[agent]
}

// SetCurrentModel sets the model reference for an agent in coordinator.
func (c *Coordinator) SetCurrentModel(agent protocol.AgentID, model string) {
	c.modelMu.Lock()
	defer c.modelMu.Unlock()
	if c.currentModel == nil {
		c.currentModel = make(map[protocol.AgentID]string)
	}
	c.currentModel[agent] = strings.TrimSpace(model)
}

// SetCurrentThinking seeds the level an agent was launched with, before a live
// bridge has had a chance to report its own thinking_state. Without it the
// cards show an empty level until the agent speaks even though the stored
// configuration already chose one.
func (c *Coordinator) SetCurrentThinking(agent protocol.AgentID, level string) {
	level = strings.TrimSpace(level)
	if level == "" {
		return
	}
	c.modelMu.Lock()
	defer c.modelMu.Unlock()
	if c.currentThinking == nil {
		c.currentThinking = make(map[protocol.AgentID]string)
	}
	c.currentThinking[agent] = level
}

func (c *Coordinator) handleThinkingState(agent protocol.AgentID, message protocol.Message) {
	level := strings.TrimSpace(message.Thinking)
	if level == "" {
		return
	}
	c.modelMu.Lock()
	if c.currentThinking == nil {
		c.currentThinking = make(map[protocol.AgentID]string)
	}
	c.currentThinking[agent] = level
	c.modelMu.Unlock()

	if c.bus != nil {
		c.bus.Emit(events.Event{Kind: events.KindThinking, Agent: agent, Thinking: level})
	}
	if c.thinkingObserver != nil {
		c.thinkingObserver(agent, level)
	}
	c.logf("%s thinking: %s", agent, level)
}

// Thinking returns the currently active thinking level for an agent.
func (c *Coordinator) Thinking(agent protocol.AgentID) string {
	c.modelMu.RLock()
	defer c.modelMu.RUnlock()
	if c.currentThinking == nil {
		return ""
	}
	return c.currentThinking[agent]
}

func (c *Coordinator) handleAgentError(agent protocol.AgentID, message protocol.Message) {
	if strings.TrimSpace(message.Text) == "" {
		return
	}
	c.tracker.Touch(agent)
	c.tracker.RecordFailure(agent)
	c.tracker.NoteError(agent, message.Text)
	c.emit(events.KindError, agent, "", message.Text)
}

func (c *Coordinator) handlePeerMessage(ctx context.Context, client *transport.Client, message protocol.Message) {
	from := client.Identity()
	to := protocol.PeerOf(from)
	if strings.TrimSpace(message.Text) == "" {
		_ = c.respond(ctx, client, message.RequestID, false, "peer message is empty", "")
		return
	}
	if to == "" {
		_ = c.respond(ctx, client, message.RequestID, false, "unknown peer for "+string(from), "")
		return
	}

	c.tracker.Touch(from)
	c.emit(events.KindPeer, from, to, message.Text)

	err := c.sendToAgent(ctx, to, protocol.Message{
		Version:   1,
		Type:      protocol.MsgSteer,
		From:      from,
		To:        to,
		Text:      message.Text,
		Timestamp: time.Now().UnixMilli(),
	})
	if err != nil {
		_ = c.respond(ctx, client, message.RequestID, false, err.Error(), c.statusText(ctx))
		return
	}

	_ = c.respond(ctx, client, message.RequestID, true, "Message sent to "+string(to)+".", c.statusText(ctx))
}

func (c *Coordinator) handleSetPlan(ctx context.Context, client *transport.Client, message protocol.Message) {
	snap, err := c.project.SetPlan(client.Identity(), message.Plan)
	if err != nil {
		_ = c.respond(ctx, client, message.RequestID, false, err.Error(), c.statusText(ctx))
		return
	}

	c.tracker.Touch(client.Identity())
	c.recordEvent("plan_updated", map[string]any{
		"agent":       string(client.Identity()),
		"planVersion": snap.PlanVersion,
	})
	c.emit(events.KindSystem, client.Identity(), "", fmt.Sprintf("updated shared plan → v%d; both signatures reset", snap.PlanVersion))
	_ = c.respond(ctx, client, message.RequestID, true,
		fmt.Sprintf("Shared plan updated to v%d. Both signatures were reset.", snap.PlanVersion), c.statusText(ctx))

	peer := protocol.PeerOf(client.Identity())
	if peer == "" {
		return
	}

	notice := fmt.Sprintf(
		"[Duo plan update]\n%s updated the shared plan to v%d.\n\n%s\n\nReview this exact version. Discuss concerns with duo_send. If you approve it, call duo_set_status with ready=true. Updating the plan again invalidates both signatures. Exploratory edits in your own worktree are allowed during PLAN, but they remain provisional until the plan is jointly approved.",
		client.Identity(), snap.PlanVersion, snap.Plan,
	)
	if c.language == "zh" {
		notice = fmt.Sprintf("[Duo plan update]\n%s 将共享计划更新为 v%d。\n\n%s\n\n请审查此精确版本，并通过 duo_send 讨论问题。批准后调用 duo_set_status ready=true。再次修改计划会使双方签署失效。PLAN 阶段允许在自己的 worktree 中探索性修改，但计划获双方批准前都属于临时工作。", client.Identity(), snap.PlanVersion, snap.Plan)
	}
	_ = c.sendToAgent(ctx, peer, protocol.Message{
		Version:   1,
		Type:      protocol.MsgDuoNotice,
		From:      protocol.Duo,
		To:        peer,
		Text:      notice,
		Timestamp: time.Now().UnixMilli(),
	})
}

// handleSetSteps takes the Step-mode queue. The message carries the raw Markdown
// checkbox list and the project package parses it once, so the queue an agent
// authored and the queue Duo enforces are the same text.
func (c *Coordinator) handleSetSteps(ctx context.Context, client *transport.Client, message protocol.Message) {
	appendOnly := message.Text == "append"
	snap, err := c.project.SetSteps(client.Identity(), message.Steps, appendOnly)
	if err != nil {
		_ = c.respond(ctx, client, message.RequestID, false, err.Error(), c.statusText(ctx))
		return
	}

	c.tracker.Touch(client.Identity())
	c.recordEvent("steps_updated", map[string]any{
		"agent": string(client.Identity()),
		"steps": len(snap.Steps),
		"step":  snap.Step,
	})
	c.emit(events.KindSystem, client.Identity(), "", fmt.Sprintf("set the step queue: %d step(s), cursor at %d", len(snap.Steps), snap.Step))
	_ = c.respond(ctx, client, message.RequestID, true,
		fmt.Sprintf("Step queue set (%d step(s), cursor at %d):\n%s", len(snap.Steps), snap.Step, snap.Steps.Render()),
		c.statusText(ctx))

	peer := protocol.PeerOf(client.Identity())
	if peer == "" {
		return
	}
	c.sendStepNotice(ctx, peer, client.Identity(), snap)
}

// sendStepNotice tells the peer the queue changed, in the session's language.
func (c *Coordinator) sendStepNotice(ctx context.Context, peer, who protocol.AgentID, snap project.Snapshot) {
	queue := snap.Steps.Render()
	notice := fmt.Sprintf(
		"[Duo step queue]\n%s set the step queue: %d step(s), cursor at %d.\n\n%s\n\nIn STEP mode the queue is the PLAN artifact: review it, then call duo_set_status with ready=true to approve it. Duo owns the ticks; agents never edit the checkboxes.",
		who, len(snap.Steps), snap.Step, queue,
	)
	if c.language == "zh" {
		notice = fmt.Sprintf(
			"[Duo 步骤队列]\n%s 设置了步骤队列：共 %d 步，当前在第 %d 步。\n\n%s\n\nSTEP 模式下队列就是 PLAN 阶段的产物：请审查后调用 duo_set_status ready=true 批准。勾选由 Duo 维护，agent 不要手改复选框。",
			who, len(snap.Steps), snap.Step, queue,
		)
	}
	_ = c.sendToAgent(ctx, peer, protocol.Message{
		Version:   1,
		Type:      protocol.MsgDuoNotice,
		From:      protocol.Duo,
		To:        peer,
		Text:      notice,
		Timestamp: time.Now().UnixMilli(),
	})
}

func (c *Coordinator) handleSetStatus(ctx context.Context, client *transport.Client, message protocol.Message) {
	// Dispatch once, at the top, by the session's mode. The Goal handler below
	// stays byte-identical to the pre-Fast implementation, and Fast never has to
	// weave conditionals through the sign-off logic.
	switch c.project.Snapshot().EffectiveMode() {
	case project.ModeFast:
		c.handleFastSetStatus(ctx, client, message)
		return
	case project.ModeStep:
		c.handleStepSetStatus(ctx, client, message)
		return
	}
	if message.Ready == nil {
		_ = c.respond(ctx, client, message.RequestID, false, "ready must be true or false", c.statusText(ctx))
		return
	}

	snapBefore := c.project.Snapshot()
	// A retried final-status message can arrive after synchronous delivery has
	// completed. Reject it without running stale-evidence reconciliation, which
	// would otherwise erase the final approval history kept in DONE.
	if snapBefore.Phase == project.PhaseDone {
		_ = c.respond(ctx, client, message.RequestID, false, project.ErrProjectDone.Error(), c.statusText(ctx))
		return
	}
	evidence := ""

	if *message.Ready {
		if err := c.invalidateStaleApprovals(ctx, snapBefore); err != nil {
			_ = c.respond(ctx, client, message.RequestID, false, err.Error(), c.statusText(ctx))
			return
		}
		// Re-read because stale signatures may have been revoked.
		snapBefore = c.project.Snapshot()
		if snapBefore.Phase == project.PhaseDone {
			_ = c.respond(ctx, client, message.RequestID, false, project.ErrProjectDone.Error(), c.statusText(ctx))
			return
		}
		var err error
		evidence, err = c.evidenceForReady(ctx, client.Identity(), snapBefore.Phase, snapBefore)
		if err != nil {
			_ = c.respond(ctx, client, message.RequestID, false, err.Error(), c.statusText(ctx))
			return
		}
	}

	snap, tr, err := c.project.SetReady(client.Identity(), *message.Ready, message.Note, evidence)
	if err != nil {
		_ = c.respond(ctx, client, message.RequestID, false, err.Error(), c.statusText(ctx))
		return
	}

	c.tracker.Touch(client.Identity())
	peer := protocol.PeerOf(client.Identity())

	// A signature is an assertion about a specific artifact in a specific phase;
	// record exactly what was signed so the journal can be audited.
	if *message.Ready {
		c.recordEvent("signature", map[string]any{
			"agent":    string(client.Identity()),
			"phase":    string(snap.Phase),
			"evidence": evidence,
		})
	} else {
		c.recordEvent("signature_revoked", map[string]any{
			"agent": string(client.Identity()),
			"phase": string(snap.Phase),
			"note":  message.Note,
		})
	}

	if tr.ReadyForDelivery {
		if c.TestCommand() != "" && c.workspace != nil {
			if testErr := c.runTestGate(ctx); testErr != nil {
				c.project.RevokeReady(client.Identity(), "automated test gate failed")
				c.recordEvent("test_gate_failed", map[string]any{"command": c.TestCommand(), "error": testErr.Error()})
				c.emit(events.KindSystem, protocol.Duo, "", fmt.Sprintf("automated test gate failed on INTEGRATE: %v", testErr))
				_ = c.respond(ctx, client, message.RequestID, false,
					fmt.Sprintf("Automated test gate (%s) failed on integrated artifact:\n%s\nFix the issue before completing.", c.TestCommand(), testErr.Error()),
					c.statusText(ctx))
				return
			}
		}
		c.handleFinalApproval(ctx, client, message, snap)
		return
	}

	if tr.Advanced {
		c.recordEvent("phase_transition", map[string]any{
			"from": string(tr.Previous),
			"to":   string(tr.Next),
		})
		c.logf("phase advanced %s → %s", tr.Previous, tr.Next)
		c.emit(events.KindSystem, protocol.Duo, "", fmt.Sprintf("%s complete by dual sign-off → %s", tr.Previous, tr.Next))

		integrationText := ""
		if tr.Next == project.PhaseIntegrate {
			integrationText = c.beginIntegration(ctx)
		}
		if tr.Next == project.PhaseDone {
			c.clearIntegrationConflict(ctx)
		}

		_ = c.respond(ctx, client, message.RequestID, true,
			fmt.Sprintf("Both agents signed %s. Duo advanced to %s.", tr.Previous, tr.Next), c.statusText(ctx))
		c.broadcastPhaseAdvance(ctx, tr.Previous, tr.Next, snap, integrationText)
		return
	}

	if *message.Ready {
		_ = c.respond(ctx, client, message.RequestID, true,
			fmt.Sprintf("%s signed %s. Waiting for %s.", client.Identity(), snap.Phase, peer), c.statusText(ctx))
	} else {
		_ = c.respond(ctx, client, message.RequestID, true,
			fmt.Sprintf("%s marked %s as not ready.", client.Identity(), snap.Phase), c.statusText(ctx))
	}

	if peer == "" {
		return
	}

	var notice string
	if *message.Ready {
		notice = fmt.Sprintf(
			"[Duo status]\n%s has signed %s. Your current status is ready=%v. If your work for this phase is genuinely complete and no unresolved issue remains, call duo_set_status with ready=true. Otherwise continue working or discuss via duo_send.",
			client.Identity(), snap.Phase, snap.Ready[peer],
		)
		if c.language == "zh" {
			notice = fmt.Sprintf("[Duo status]\n%s 已签署 %s。你当前的状态为 ready=%v。如果本阶段负责的工作确已完成且没有未解决问题，请调用 duo_set_status ready=true；否则继续工作或通过 duo_send 讨论。", client.Identity(), snap.Phase, snap.Ready[peer])
		}
	} else {
		notice = fmt.Sprintf(
			"[Duo status]\n%s revoked readiness for %s%s. Continue collaboration until the issue is resolved.",
			client.Identity(), snap.Phase, noteSuffix(message.Note),
		)
		if c.language == "zh" {
			notice = fmt.Sprintf("[Duo status]\n%s 撤销了 %s 阶段的就绪状态%s。请继续协作，直到问题解决。", client.Identity(), snap.Phase, noteSuffix(message.Note))
		}
	}
	_ = c.sendToAgent(ctx, peer, protocol.Message{
		Version:   1,
		Type:      protocol.MsgDuoNotice,
		From:      protocol.Duo,
		To:        peer,
		Text:      notice,
		Timestamp: time.Now().UnixMilli(),
	})
}

// handleFastSetStatus is the Fast counterpart of handleSetStatus. In Fast the
// ready flag is not a vote: it is Austin's completion request, which binds the
// exact Austin HEAD Tony must verify, or the withdrawal of that request.
func (c *Coordinator) handleFastSetStatus(ctx context.Context, client *transport.Client, message protocol.Message) {
	if message.Ready == nil {
		_ = c.respond(ctx, client, message.RequestID, false, "ready must be true or false", c.statusText(ctx))
		return
	}

	snap := c.project.Snapshot()
	if snap.Phase == project.PhaseDone {
		if !c.reopenFastRoundForNewWork(ctx, client, message) {
			_ = c.respond(ctx, client, message.RequestID, false, project.ErrProjectDone.Error(), c.statusText(ctx))
			return
		}
		snap = c.project.Snapshot()
	}
	if client.Identity() != protocol.Austin {
		_ = c.respond(ctx, client, message.RequestID, false,
			"Fast mode has no phase sign-off. Tony reports an independent verdict with duo_set_verification once Austin requests verification.", c.statusText(ctx))
		return
	}

	evidence := ""
	if *message.Ready {
		if snap.Phase == project.PhaseVerify {
			// Idempotent retry: keep the HEAD already under verification.
			evidence = snap.Verification.Head
		} else {
			head, err := recovery.FastVerificationTarget(ctx, c.workspace)
			if err != nil {
				_ = c.respond(ctx, client, message.RequestID, false, err.Error(), c.statusText(ctx))
				return
			}
			evidence = head

			if c.TestCommand() != "" && c.workspace != nil {
				c.emit(events.KindSystem, protocol.Duo, "", fmt.Sprintf("running automated test gate: %s", c.TestCommand()))
				if testErr := c.runTestGate(ctx); testErr != nil {
					c.recordEvent("test_gate_failed", map[string]any{"command": c.TestCommand(), "error": testErr.Error()})
					c.emit(events.KindSystem, protocol.Duo, "", fmt.Sprintf("automated test gate failed: %v", testErr))
					_ = c.respond(ctx, client, message.RequestID, false,
						fmt.Sprintf("Automated test gate (%s) failed:\n%s\nFix the test failure before requesting verification.", c.TestCommand(), testErr.Error()),
						c.statusText(ctx))
					return
				}
				c.recordEvent("test_gate_passed", map[string]any{"command": c.TestCommand()})
				c.emit(events.KindSystem, protocol.Duo, "", fmt.Sprintf("automated test gate passed: %s", c.TestCommand()))
			}
		}
	}

	snap, tr, err := c.project.SetReady(client.Identity(), *message.Ready, message.Note, evidence)
	if err != nil {
		_ = c.respond(ctx, client, message.RequestID, false, err.Error(), c.statusText(ctx))
		return
	}
	c.tracker.Touch(client.Identity())

	if !*message.Ready {
		c.recordEvent("completion_withdrawn", map[string]any{"phase": string(snap.Phase)})
		_ = c.respond(ctx, client, message.RequestID, true, "Completion request withdrawn.", c.statusText(ctx))
		text := "[Duo status]\nAustin withdrew the verification request. Stand by; no verification is pending."
		if c.language == "zh" {
			text = "[Duo status]\nAustin 已撤回验证请求。请待命；当前没有待验证事项。"
		}
		c.sendFastNotice(ctx, protocol.Tony, text)
		return
	}

	c.recordEvent("verification_requested", map[string]any{"head": snap.Verification.Head})
	if tr.Advanced {
		c.recordEvent("phase_transition", map[string]any{"from": string(tr.Previous), "to": string(tr.Next)})
		c.logf("[FAST] phase advanced %s → %s", tr.Previous, tr.Next)
		c.emit(events.KindSystem, protocol.Duo, "", fmt.Sprintf("verification requested → %s", tr.Next))
		c.attention(events.AttentionVerification)
	}
	_ = c.respond(ctx, client, message.RequestID, true,
		fmt.Sprintf("Verification requested for Austin HEAD %s. Tony must independently verify before Duo delivers.", shortSHA(snap.Verification.Head)),
		c.statusText(ctx))
	text := fmt.Sprintf(
		"[Duo verification request]\nAustin reports the work complete and requested independent verification of HEAD %s.\n\nInspect that exact artifact yourself. Then report a structured verdict with duo_set_verification: result=passed, or result=issue_found with a concrete note describing the problem. Fast mode is single-writer: do not commit or edit the artifact; if a fix is needed, report it so Austin applies it.",
		shortSHA(snap.Verification.Head),
	)
	if c.language == "zh" {
		text = fmt.Sprintf("[Duo verification request]\nAustin 已报告工作完成，并请求独立验证 HEAD %s。\n\n请独立检查该精确产物，然后通过 duo_set_verification 给出结构化结论：result=passed，或 result=issue_found 并附上具体问题。Fast 模式只有 Austin 可以写入：不要提交或编辑产物；需要修复时报告问题，由 Austin 修改。", shortSHA(snap.Verification.Head))
	}
	c.sendFastNotice(ctx, protocol.Tony, text)
}

// reopenFastRoundForNewWork detects a Fast round that was driven outside the Duo
// composer. The composer reopens a DONE session through SubmitUserTask, but the
// human can also talk to Austin through a native attach (Ctrl+A) and let it commit; that
// path never reaches Duo, so Austin's next completion request is the only signal
// of the new round. A request that names a different Austin HEAD than the
// delivered round is genuine new work: reopen the finished round and let the
// caller handle it as an ordinary RUNNING → VERIFY request. A retry that still
// names the delivered HEAD is a duplicate of the finished round and stays
// rejected, so a stray resend can never undo a delivery.
func (c *Coordinator) reopenFastRoundForNewWork(ctx context.Context, client *transport.Client, message protocol.Message) bool {
	if message.Ready == nil || !*message.Ready || client.Identity() != protocol.Austin {
		return false
	}
	delivered := strings.TrimSpace(c.project.Snapshot().Verification.Head)
	if delivered == "" {
		return false
	}
	head, err := recovery.FastVerificationTarget(ctx, c.workspace)
	if err != nil || head == delivered {
		return false
	}
	c.reopenFinishedSession()
	return true
}

// handleSetVerification is the Fast-mode verification gate. It accepts exactly
// one structured verdict from Tony while the session is in VERIFY, bound to the
// Austin HEAD that Austin asked to have verified.
func (c *Coordinator) handleSetVerification(ctx context.Context, client *transport.Client, message protocol.Message) {
	if c.project.Snapshot().EffectiveMode() == project.ModeStep {
		c.handleStepVerification(ctx, client, message)
		return
	}
	if c.project.Snapshot().EffectiveMode() != project.ModeFast {
		_ = c.respond(ctx, client, message.RequestID, false,
			"structured verification is only used in Fast mode", c.statusText(ctx))
		return
	}
	if client.Identity() != protocol.Tony {
		_ = c.respond(ctx, client, message.RequestID, false,
			"only Tony reports Fast-mode verification", c.statusText(ctx))
		return
	}

	result, err := project.ParseVerificationResult(message.Verification)
	if err != nil {
		_ = c.respond(ctx, client, message.RequestID, false, err.Error(), c.statusText(ctx))
		return
	}
	if result == project.VerificationIssueFound && strings.TrimSpace(message.Note) == "" {
		_ = c.respond(ctx, client, message.RequestID, false, project.ErrMissingIssue.Error(), c.statusText(ctx))
		return
	}

	snap := c.project.Snapshot()
	if snap.Phase == project.PhaseDone {
		_ = c.respond(ctx, client, message.RequestID, false, project.ErrProjectDone.Error(), c.statusText(ctx))
		return
	}
	if snap.Phase != project.PhaseVerify {
		_ = c.respond(ctx, client, message.RequestID, false,
			"verification can only be reported during VERIFY", c.statusText(ctx))
		return
	}

	// The verdict has to describe the artifact Austin actually asked to have
	// verified. If Austin committed after the request, the request is stale:
	// revoke it and return to RUNNING so the new HEAD is verified on a fresh
	// request. This is the same deterministic rule recovery applies on resume.
	target, err := recovery.FastVerificationTarget(ctx, c.workspace)
	if err != nil || target != strings.TrimSpace(snap.Verification.Head) {
		c.project.RevokeReady(protocol.Austin, "the artifact changed after verification was requested; request verification again")
		c.recordEvent("verification_revoked", map[string]any{
			"requested": snap.Verification.Head,
			"current":   target,
		})
		c.emit(events.KindSystem, protocol.Duo, "", "the verified HEAD changed; Fast verification was revoked and the session returned to RUNNING")
		reason := "Austin's HEAD changed after verification was requested, so the request was revoked and the session returned to RUNNING. Ask Austin to request verification again."
		if err != nil {
			reason = err.Error() + "; the verification request was revoked and the session returned to RUNNING."
		}
		_ = c.respond(ctx, client, message.RequestID, false, reason, c.statusText(ctx))
		return
	}

	snap, tr, err := c.project.SetVerification(client.Identity(), result, message.Note, target)
	if err != nil {
		_ = c.respond(ctx, client, message.RequestID, false, err.Error(), c.statusText(ctx))
		return
	}
	c.tracker.Touch(protocol.Tony)

	switch result {
	case project.VerificationPassed:
		if c.TestCommand() != "" && c.workspace != nil {
			if testErr := c.runTestGate(ctx); testErr != nil {
				c.recordEvent("test_gate_failed", map[string]any{"command": c.TestCommand(), "error": testErr.Error()})
				c.emit(events.KindSystem, protocol.Duo, "", fmt.Sprintf("automated test gate failed before delivery: %v", testErr))
				_ = c.respond(ctx, client, message.RequestID, false,
					fmt.Sprintf("Automated test gate (%s) failed before delivery:\n%s\nFix the issue before verifying.", c.TestCommand(), testErr.Error()),
					c.statusText(ctx))
				return
			}
		}
		c.recordEvent("verification", map[string]any{"result": "passed", "head": target})
		c.emit(events.KindSystem, protocol.Duo, "", fmt.Sprintf("Tony independently verified %s → passed", shortSHA(target)))
		_ = c.respond(ctx, client, message.RequestID, true,
			"Verification passed. Duo is delivering the verified artifact to the user's repository before marking DONE.",
			c.statusText(ctx))
		if tr.ReadyForDelivery {
			c.deliverFinal(ctx, snap)
		}

	case project.VerificationIssueFound:
		c.recordEvent("verification", map[string]any{"result": "issue_found", "note": message.Note})
		// issue_found is the verifier doing its job, not a Duo failure: surface it
		// as a verdict so the TUI warns instead of reporting a red ERROR.
		c.emit(events.KindVerdict, protocol.Tony, protocol.Austin, message.Note)
		_ = c.respond(ctx, client, message.RequestID, true,
			"Issue recorded. The session returned to RUNNING; Austin continues and must request verification again when the fix is complete.",
			c.statusText(ctx))
		c.steerToAustin(ctx, fmt.Sprintf(
			"[Duo verification] Tony found an issue with %s:\n\n%s\n\nFix it, commit, and then request verification again with duo_set_status ready=true. Do not assume the issue is settled until Tony verifies the new HEAD.",
			shortSHA(target), strings.TrimSpace(message.Note),
		))
	}
}

func (c *Coordinator) handleEscalate(ctx context.Context, client *transport.Client, message protocol.Message) {
	reason := strings.TrimSpace(message.Note)
	if reason == "" {
		reason = strings.TrimSpace(message.Text)
	}
	if err := c.SwitchMode(ctx, project.ModeGoal, reason); err != nil {
		_ = c.respond(ctx, client, message.RequestID, false, err.Error(), c.statusText(ctx))
		return
	}
	_ = c.respond(ctx, client, message.RequestID, true, "switched to Goal mode successfully; open duo_status for the current phase", c.statusText(ctx))
}

func (c *Coordinator) sendFastNotice(ctx context.Context, agent protocol.AgentID, text string) {
	_ = c.sendToAgent(ctx, agent, protocol.Message{
		Version:   protocol.Version,
		Type:      protocol.MsgDuoNotice,
		From:      protocol.Duo,
		To:        agent,
		Text:      text,
		Timestamp: time.Now().UnixMilli(),
	})
}

func (c *Coordinator) steerToAustin(ctx context.Context, text string) {
	_ = c.sendToAgent(ctx, protocol.Austin, protocol.Message{
		Version:   protocol.Version,
		Type:      protocol.MsgSteer,
		From:      protocol.Tony,
		To:        protocol.Austin,
		Text:      text,
		Timestamp: time.Now().UnixMilli(),
	})
}

// handleFinalApproval runs the delivery transaction after both agents signed
// INTEGRATE. The final approval is already durable at this point; Duo now hands
// the exact approved artifact back to the user's repository and only then marks
// the project DONE.
func (c *Coordinator) handleFinalApproval(
	ctx context.Context,
	client *transport.Client,
	message protocol.Message,
	snap project.Snapshot,
) {
	c.recordEvent("final_approval", map[string]any{
		"austin": snap.Evidence[protocol.Austin],
		"tony":   snap.Evidence[protocol.Tony],
	})
	c.emit(events.KindSystem, protocol.Duo, "", "both agents approved the final integrated HEAD; Duo is delivering it to the original repository")

	_ = c.respond(ctx, client, message.RequestID, true,
		"Both agents approved the final integrated HEAD. Duo is delivering the result to the original repository before marking DONE.",
		c.statusText(ctx))

	c.broadcastFinalApproval(ctx, snap)
	c.deliverFinal(ctx, snap)
}

// deliverFinal performs the delivery transaction. It never resets approvals and
// never marks DONE unless the artifact actually landed in the user's repo.
func (c *Coordinator) deliverFinal(ctx context.Context, _ project.Snapshot) {
	c.deliveryTxnMu.Lock()
	defer c.deliveryTxnMu.Unlock()

	// Do not trust the state that caused this call: a duplicate/reconnect may
	// have completed delivery while this handler waited for the transaction.
	snap := c.project.Snapshot()
	currentDelivery := c.CurrentDelivery()
	if snap.Phase == project.PhaseDone {
		return
	}
	if currentDelivery.Applied() && !stepDeliveryStale(snap, currentDelivery) {
		if snap.EffectiveMode() == project.ModeStep {
			// The step already landed on the base branch but the session transition
			// did not persist. Replay it through the domain rule, which is idempotent
			// on the step record, then decide whether the queue is finished.
			landed, moved, err := c.landStepIfNeeded(currentDelivery.FinalHead)
			if err != nil {
				c.logf("reconcile applied step landing: %v", err)
				return
			}
			if moved {
				if err := c.persistStrict("step landed"); err != nil {
					c.logf("persist reconciled step landing: %v", err)
				}
				c.recordEvent("step_landed", map[string]any{
					"step":        snap.Step,
					"finalHead":   currentDelivery.FinalHead,
					"appliedHead": currentDelivery.AppliedHead,
					"reconciled":  true,
				})
			}
			if !project.QueueFinished(landed.Steps) {
				return
			}
			snap = landed
		}
		if snap.Phase == project.PhaseIntegrate || snap.Phase == project.PhaseVerify {
			if _, err := c.project.Complete(); err != nil {
				c.logf("complete applied delivery: %v", err)
				return
			}
			if err := c.persistStrict("project complete"); err != nil {
				c.logf("persist completed applied delivery: %v", err)
			}
			c.requestFinalSummary(ctx)
		}
		return
	}

	// The work must be complete and every approval must still describe the
	// artifact about to be delivered. Each mode has its own gate: Fast has one
	// verification bound to Austin's HEAD, Goal has a dual INTEGRATE sign-off, and
	// STEP lands each step on Tony's HEAD-bound verdict for that step — only the
	// final step of the queue needs both agents to agree. Everything after this
	// point is the same idempotent delivery transaction for all three.
	fast := snap.EffectiveMode() == project.ModeFast
	switch snap.EffectiveMode() {
	case project.ModeFast:
		if snap.Phase != project.PhaseVerify {
			return
		}
	case project.ModeStep:
		if snap.Phase != project.PhaseIntegrate {
			return
		}
		// A FAST step mid-queue lands on Tony's verdict. A GOAL step lands on
		// both agents' INTEGRATE signatures. The final step always needs both
		// agents to sign the finished queue, whatever its per-step policy.
		if (stepIsFinal(snap) || currentStepPolicy(snap) == project.ModeGoal) &&
			(!snap.Ready[protocol.Austin] || !snap.Ready[protocol.Tony]) {
			return
		}
	default:
		if snap.Phase != project.PhaseIntegrate || !snap.Ready[protocol.Austin] || !snap.Ready[protocol.Tony] {
			return
		}
	}
	set := c.workspace.Set()

	finalHead, err := c.workspace.Head(ctx, protocol.Austin)
	if err != nil || strings.TrimSpace(finalHead) == "" {
		reason := "cannot resolve the final integrated HEAD"
		if fast {
			reason = "cannot resolve the verified HEAD"
		}
		if err != nil {
			reason += ": " + err.Error()
		}
		if err := c.markDeliveryPending(ctx, currentDelivery, reason); err != nil {
			c.logf("persist unresolved delivery: %v", err)
		}
		return
	}

	// Each mode compares the delivered HEAD with the artifact its gate approved.
	switch snap.EffectiveMode() {
	case project.ModeFast:
		// Tony's verdict describes one exact Austin HEAD. If that HEAD moved after
		// verification, the verdict no longer covers the artifact and delivery
		// must not proceed: revoke it and return to RUNNING.
		if !snap.Verification.Passed(finalHead) {
			c.project.RevokeReady(protocol.Austin, "the verified artifact changed before delivery; verification revoked")
			c.recordEvent("verification_revoked", map[string]any{
				"verified": snap.Verification.Head,
				"current":  finalHead,
			})
			if err := c.markDeliveryPending(ctx, sessionstore.Delivery{}, "the verified HEAD changed before delivery; Austin must request verification again"); err != nil {
				c.logf("persist revoked verification: %v", err)
			}
			return
		}

	case project.ModeStep:
		// A FAST step mid-queue uses the step's own submitted HEAD, never Austin's
		// live HEAD: landing the live HEAD would deliver commits Tony never
		// reviewed. GOAL steps and every final step use the signatures, which bind
		// the exact integrated HEAD the agents approved.
		if !stepIsFinal(snap) && currentStepPolicy(snap) == project.ModeFast {
			stepHead := ""
			if snap.Step >= 1 && snap.Step <= len(snap.Steps) {
				stepHead = strings.TrimSpace(snap.Steps[snap.Step-1].Head)
			}
			if stepHead == "" || stepHead != finalHead {
				c.revokeStepSubmission(ctx, snap, stepHead, finalHead)
				return
			}
			break
		}
		for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
			if strings.TrimSpace(snap.Evidence[agent]) != finalHead {
				c.revokeIntegrateSignatures(ctx, snap, finalHead)
				return
			}
		}

	default:
		// Whoever signed INTEGRATE signed this exact HEAD. If it moved between the
		// two signatures, the approvals are no longer about one artifact.
		for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
			if strings.TrimSpace(snap.Evidence[agent]) != finalHead {
				c.project.RevokeReady(agent, "final integrated HEAD changed before delivery; readiness revoked")
				c.recordEvent("signature_revoked", map[string]any{
					"agent":  string(agent),
					"phase":  string(project.PhaseIntegrate),
					"reason": "final integrated HEAD changed before delivery",
				})
				if err := c.markDeliveryPending(ctx, sessionstore.Delivery{}, "the final integrated HEAD changed before delivery; both agents must sign again"); err != nil {
					c.logf("persist revoked delivery: %v", err)
				}
				return
			}
		}
	}

	now := time.Now().UTC()
	pending := sessionstore.Delivery{
		Status:       sessionstore.DeliveryPending,
		FinalHead:    finalHead,
		FinalBranch:  set.Austin.Branch,
		TargetRepo:   set.Repository,
		TargetBranch: set.BaseBranch,
		AttemptedAt:  &now,
	}

	// Persist the final approval and the pending delivery checkpoint before
	// touching the original repository. If Duo crashes after this point, resume
	// can prove the approval happened and re-run an idempotent delivery.
	c.SetDelivery(pending)
	if err := c.persistStrict("pending delivery checkpoint"); err != nil {
		c.logf("delivery not started: %v", err)
		c.emit(events.KindError, protocol.Duo, "", "delivery not started: "+err.Error())
		c.attention(events.AttentionDelivery)
		return
	}
	c.recordEvent("delivery_attempt", map[string]any{
		"finalHead":    finalHead,
		"finalBranch":  set.Austin.Branch,
		"targetRepo":   set.Repository,
		"targetBranch": set.BaseBranch,
	})
	c.logf("delivering %s back to %s@%s", shortSHA(finalHead), set.Repository, set.BaseBranch)

	manager := delivery.Manager{
		Repository:  set.Repository,
		BaseBranch:  set.BaseBranch,
		BaseCommit:  set.BaseCommit,
		FinalBranch: set.Austin.Branch,
		FinalHead:   finalHead,
	}

	result, err := manager.Deliver(ctx)
	if err != nil {
		if persistErr := c.markDeliveryPending(ctx, pending, "delivery failed: "+err.Error()); persistErr != nil {
			c.logf("persist failed delivery: %v", persistErr)
		}
		return
	}
	if !result.Applied {
		if err := c.markDeliveryPending(ctx, pending, result.Check.Reason); err != nil {
			c.logf("persist pending delivery: %v", err)
		}
		return
	}

	if err := c.completeDelivery(ctx, pending, result); err != nil {
		c.logf("complete delivery: %v", err)
	}
}

// stepDeliveryStale reports whether an applied delivery record describes an
// earlier step than the one under the cursor. A Step session delivers once per
// step, so the record is stale by design on every step after the first — the exact
// reason `Delivery.Applied()` cannot mean "the session is finished" in STEP mode.
func stepDeliveryStale(snap project.Snapshot, record sessionstore.Delivery) bool {
	if snap.EffectiveMode() != project.ModeStep || project.QueueFinished(snap.Steps) {
		return false
	}
	if snap.Step < 1 || snap.Step > len(snap.Steps) {
		return true
	}
	step := snap.Steps[snap.Step-1]
	return !(step.Done && step.DeliveryHead == record.FinalHead)
}

// landStepIfNeeded ticks the step the cursor is on, and reports whether it moved.
// It is idempotent on the step record, so a replayed delivery can never advance the
// queue twice.
func (c *Coordinator) landStepIfNeeded(head string) (project.Snapshot, bool, error) {
	snap := c.project.Snapshot()
	if snap.EffectiveMode() != project.ModeStep {
		return snap, false, nil
	}
	if snap.Step >= 1 && snap.Step <= len(snap.Steps) {
		if step := snap.Steps[snap.Step-1]; step.Done && step.DeliveryHead == head {
			return snap, false, nil
		}
	}
	landed, _, err := c.project.LandStep(head)
	return landed, true, err
}

// markDeliveryPending records a blocked delivery without ever claiming DONE and
// without modifying the user's working tree.
func (c *Coordinator) markDeliveryPending(ctx context.Context, record sessionstore.Delivery, reason string) error {
	if current := c.CurrentDelivery(); current.Applied() && current.FinalHead == record.FinalHead {
		return nil
	}
	record.Status = sessionstore.DeliveryPending
	record.Reason = strings.TrimSpace(reason)
	if record.AttemptedAt == nil {
		now := time.Now().UTC()
		record.AttemptedAt = &now
	}
	c.SetDelivery(record)
	if err := c.persistStrict("delivery pending checkpoint"); err != nil {
		c.logf("persist delivery pending: %v", err)
		c.emit(events.KindError, protocol.Duo, "", "failed to persist delivery checkpoint: "+err.Error())
		return err
	}
	c.recordEvent("delivery_pending", map[string]any{
		"reason":    record.Reason,
		"finalHead": record.FinalHead,
	})
	c.logf("delivery pending: %s", record.Reason)
	c.emit(events.KindError, protocol.Duo, "",
		"Agent work is complete, but delivery is pending: "+record.Reason+
			". No user files were overwritten. After resolving the original repository, run `duo apply`.")
	c.notifyDeliveryPending(ctx, record)
	return nil
}

// completeDelivery persists the applied checkpoint, performs the explicit
// INTEGRATE → DONE transition and tells both agents that the hand-off is done.
func (c *Coordinator) completeDelivery(ctx context.Context, record sessionstore.Delivery, result delivery.Result) error {
	now := time.Now().UTC()

	// The delivered tree is the final integration result, so any recorded merge
	// conflict is resolved by definition.
	c.clearIntegrationConflict(ctx)

	applied := record
	applied.Status = sessionstore.DeliveryApplied
	applied.AppliedHead = result.Check.CurrentHead
	applied.Reason = ""
	applied.CompletedAt = &now
	c.SetDelivery(applied)
	if err := c.persistStrict("delivery applied checkpoint"); err != nil {
		c.logf("persist delivery applied: %v", err)
		c.emit(events.KindError, protocol.Duo, "", "delivery applied but its checkpoint could not be persisted: "+err.Error())
		return err
	}

	// The phase the work was approved in is INTEGRATE in Goal and STEP and VERIFY
	// in Fast; capture it before the transition so the notice names the real edge.
	previous := c.project.Snapshot().Phase

	// A STEP session lands one step per delivery. Landing ticks the step and
	// returns to EXECUTE while the queue has work; only a finished queue completes
	// the session, so this is the mode's loop-back rather than a second rule.
	if pre := c.project.Snapshot(); pre.EffectiveMode() == project.ModeStep {
		landed, moved, err := c.landStepIfNeeded(applied.FinalHead)
		if err != nil {
			if persistErr := c.markDeliveryPending(ctx, applied, "delivery applied but the step could not be landed: "+err.Error()); persistErr != nil {
				return fmt.Errorf("land step: %w (persist pending: %v)", err, persistErr)
			}
			return fmt.Errorf("land step: %w", err)
		}
		if !moved {
			c.logf("step %d already recorded as landed at %s", pre.Step, shortSHA(applied.FinalHead))
		}
		if err := c.persistStrict("step landed"); err != nil {
			return err
		}
		c.recordEvent("step_landed", map[string]any{
			"step":         pre.Step,
			"finalHead":    applied.FinalHead,
			"appliedHead":  applied.AppliedHead,
			"targetBranch": applied.TargetBranch,
		})
		c.logf("landed step %d of %d at %s on %s", pre.Step, len(pre.Steps), shortSHA(applied.AppliedHead), applied.TargetBranch)
		c.emit(events.KindSystem, protocol.Duo, "",
			fmt.Sprintf("step %d of %d landed on %s@%s", pre.Step, len(pre.Steps), applied.TargetBranch, shortSHA(applied.AppliedHead)))
		c.reportChanges(result.Changes)
		// The landing is now durable, which is the only point where the verifier's
		// worktree can be proven to match the tree the user received. Doing it here,
		// on the delivery itself, covers every path that can land a step rather than
		// only the one that submitted the verdict.
		c.syncVerifierToLandedStep(ctx, applied.AppliedHead)
		if !project.QueueFinished(landed.Steps) {
			c.broadcastPhaseAdvance(ctx, previous, landed.Phase, landed, "")
			return nil
		}
		// The queue is finished, so the last step still needs the dual sign-off that
		// stepQueueSignOff recorded before calling here.
		done, err := c.project.Complete()
		if err != nil {
			if persistErr := c.markDeliveryPending(ctx, applied, "the queue is finished but the session could not complete: "+err.Error()); persistErr != nil {
				return fmt.Errorf("complete delivery: %w (persist pending: %v)", err, persistErr)
			}
			return fmt.Errorf("complete delivery: %w", err)
		}
		if err := c.persistStrict("project complete"); err != nil {
			return err
		}
		c.recordEvent("delivery_applied", map[string]any{
			"finalHead":    applied.FinalHead,
			"appliedHead":  applied.AppliedHead,
			"targetRepo":   applied.TargetRepo,
			"targetBranch": applied.TargetBranch,
			"changedFiles": len(result.Changes),
		})
		c.logf("delivered %s to %s@%s (%d changed file(s))",
			shortSHA(applied.AppliedHead), applied.TargetRepo, applied.TargetBranch, len(result.Changes))
		c.emit(events.KindSystem, protocol.Duo, "",
			fmt.Sprintf("delivery complete → %s@%s", applied.TargetBranch, shortSHA(applied.AppliedHead)))
		c.broadcastPhaseAdvance(ctx, previous, project.PhaseDone, done, "")
		c.requestFinalSummary(ctx)
		return nil
	}

	done, err := c.project.Complete()
	if err != nil {
		if persistErr := c.markDeliveryPending(ctx, applied, "delivery applied but DONE transition failed: "+err.Error()); persistErr != nil {
			return fmt.Errorf("complete delivery: %w (persist pending: %v)", err, persistErr)
		}
		return fmt.Errorf("complete delivery: %w", err)
	}
	if err := c.persistStrict("project complete"); err != nil {
		return err
	}

	c.recordEvent("delivery_applied", map[string]any{
		"finalHead":    applied.FinalHead,
		"appliedHead":  applied.AppliedHead,
		"targetRepo":   applied.TargetRepo,
		"targetBranch": applied.TargetBranch,
		"changedFiles": len(result.Changes),
	})
	c.logf("delivered %s to %s@%s (%d changed file(s))",
		shortSHA(applied.AppliedHead), applied.TargetRepo, applied.TargetBranch, len(result.Changes))
	c.emit(events.KindSystem, protocol.Duo, "",
		fmt.Sprintf("delivery complete → %s@%s", applied.TargetBranch, shortSHA(applied.AppliedHead)))
	c.reportChanges(result.Changes)
	c.broadcastPhaseAdvance(ctx, previous, project.PhaseDone, done, "")
	// The round is finished and delivered, so the human gets the account of it
	// from the agent that did the work rather than from Duo's own notices.
	c.requestFinalSummary(ctx)
	return nil
}

// reportChanges records the delivered change set for auditability.
func (c *Coordinator) reportChanges(changes []delivery.Change) {
	files := make([]string, 0, len(changes))
	for _, change := range changes {
		files = append(files, change.String())
	}
	c.recordEvent("delivery_changes", map[string]any{"files": files})
	c.logf("delivered change set:\n%s", delivery.Summary(changes))
}

func (c *Coordinator) evidenceForReady(
	ctx context.Context,
	agent protocol.AgentID,
	phase project.Phase,
	snap project.Snapshot,
) (string, error) {
	// Live signing and crash recovery share one rule so a resumed session can
	// never revoke a signature that signing would have considered valid.
	return recovery.ExpectedEvidence(ctx, c.workspace, phase, agent, snap.PlanVersion)
}

// invalidateStaleApprovals makes a signature mean something concrete.
// EXECUTE signs the agent's own HEAD; REVIEW signs the peer's HEAD; INTEGRATE
// signs Austin's integrated HEAD. If that target changes before the second
// signature arrives, the old signature is revoked instead of silently advancing.
func (c *Coordinator) invalidateStaleApprovals(ctx context.Context, snap project.Snapshot) error {
	for _, signer := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		if !snap.Ready[signer] {
			continue
		}
		expected, err := recovery.ExpectedEvidence(ctx, c.workspace, snap.Phase, signer, snap.PlanVersion)
		if err != nil || strings.TrimSpace(expected) == "" || expected != snap.Evidence[signer] {
			reason := "signed target changed; readiness revoked"
			if err != nil {
				reason = "signed target is no longer clean/reviewable; readiness revoked"
			}
			c.project.RevokeReady(signer, reason)
			c.recordEvent("signature_revoked", map[string]any{
				"agent":  string(signer),
				"phase":  string(snap.Phase),
				"reason": reason,
			})
			c.emit(events.KindSystem, signer, "", fmt.Sprintf("revoked stale %s signature", snap.Phase))
		}
	}
	return nil
}

func (c *Coordinator) beginIntegration(ctx context.Context) string {
	c.recordEvent("merge_started", map[string]any{"phase": string(project.PhaseReview)})

	result, err := c.workspace.IntegrateTonyIntoAustin(ctx)
	if err != nil {
		c.recordEvent("merge_failed", map[string]any{"error": err.Error()})
		text := "Integration could not start automatically: " + err.Error()
		if c.language == "zh" {
			text = "无法自动开始集成：" + err.Error()
		}
		c.emit(events.KindError, protocol.Duo, "", text)
		return text
	}

	c.SetIntegration(result)

	if result.Conflicted {
		c.recordEvent("merge_conflict", map[string]any{
			"head":       result.Head,
			"mergedTony": result.MergedTony,
		})
		c.persistNow("merge conflict")
		text := fmt.Sprintf(
			"Git merge started in Austin's worktree but has conflicts. Austin must resolve them in %s, commit the resolution, run validation, and then sign INTEGRATE. Tony must review Austin's final integrated HEAD before signing.",
			result.AustinPath,
		)
		if c.language == "zh" {
			text = fmt.Sprintf("Git 已在 Austin 的 worktree 中开始合并，但存在冲突。Austin 必须在 %s 中解决冲突、提交解决结果、运行验证，然后签署 INTEGRATE。Tony 必须在签署前审查 Austin 最终集成的 HEAD。", result.AustinPath)
		}
		c.emit(events.KindSystem, protocol.Duo, "", "integration conflict in "+result.AustinPath)
		c.attention(events.AttentionConflict)
		return text
	}

	c.recordEvent("merge_complete", map[string]any{
		"head":       result.Head,
		"mergedTony": result.MergedTony,
	})
	c.persistNow("integration merge")

	text := fmt.Sprintf(
		"Tony branch merged into Austin integration branch %s. Integrated HEAD is %s. Austin should run final validation; Tony should review this integrated branch. Both sign INTEGRATE only after the final HEAD is acceptable.",
		result.AustinBranch,
		shortSHA(result.Head),
	)
	if c.language == "zh" {
		text = fmt.Sprintf("Tony 的分支已合并到 Austin 的集成分支 %s。集成后的 HEAD 为 %s。Austin 应运行最终验证；Tony 应审查该集成分支。只有最终 HEAD 可接受时双方才能签署 INTEGRATE。", result.AustinBranch, shortSHA(result.Head))
	}
	c.emit(events.KindSystem, protocol.Duo, "", fmt.Sprintf("integration merge complete → %s@%s", result.AustinBranch, shortSHA(result.Head)))
	return text
}

// clearIntegrationConflict marks the integration as resolved once both agents
// have signed the final integrated HEAD, so a resumed session never reports a
// stale conflict.
func (c *Coordinator) clearIntegrationConflict(ctx context.Context) {
	status, err := c.workspace.Status(ctx, protocol.Austin)
	if err != nil {
		c.logf("could not confirm integrated HEAD after DONE: %v", err)
		return
	}
	c.integrationMu.Lock()
	c.integration.Conflicted = false
	if status.Head != "" {
		c.integration.Head = status.Head
	}
	result := c.integration
	c.integrationMu.Unlock()
	c.recordEvent("integration_resolved", map[string]any{"head": result.Head})
	c.persistNow("integration resolved")
}

func (c *Coordinator) handleGetStatus(ctx context.Context, client *transport.Client, message protocol.Message) {
	c.tracker.Touch(client.Identity())
	_ = c.respond(ctx, client, message.RequestID, true, "Current Duo state.", c.statusText(ctx))
}

func (c *Coordinator) statusText(ctx context.Context) string {
	parts := []string{c.project.Snapshot().String()}
	if c.workspace != nil {
		parts = append(parts, c.workspace.Set().String())
		for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
			if status, err := c.workspace.Status(ctx, agent); err == nil {
				parts = append(parts, status.String())
			}
		}
	}
	return strings.Join(parts, "\n")
}

func (c *Coordinator) respond(ctx context.Context, client *transport.Client, requestID string, ok bool, text, state string) error {
	if requestID == "" {
		return nil
	}
	if c.language == "zh" {
		text = chineseResponse(text)
	}
	return client.Send(ctx, protocol.Message{
		Version:   1,
		Type:      protocol.MsgResponse,
		RequestID: requestID,
		OK:        ok,
		Text:      text,
		State:     state,
		Timestamp: time.Now().UnixMilli(),
	})
}

func chineseResponse(text string) string {
	switch {
	case text == "ready must be true or false":
		return "ready 必须是 true 或 false"
	case text == "Message sent to Austin." || text == "Message sent to Tony.":
		return strings.TrimSuffix(strings.TrimPrefix(text, "Message sent to "), ".") + " 的消息已发送。"
	case strings.HasPrefix(text, "Shared plan updated to v"):
		version, _, _ := strings.Cut(strings.TrimPrefix(text, "Shared plan updated to v"), ".")
		return "共享计划已更新为 v" + version + "，双方签署已重置。"
	case strings.HasPrefix(text, "Automated test gate ("):
		return strings.Replace(text, "Automated test gate", "自动测试门禁", 1)
	case strings.HasPrefix(text, "Verification requested for Austin HEAD "):
		head, _, _ := strings.Cut(strings.TrimPrefix(text, "Verification requested for Austin HEAD "), ". Tony must independently verify before Duo delivers.")
		return "已请求验证 Austin HEAD " + head + "。交付前 Tony 必须独立验证。"
	case text == "Completion request withdrawn.":
		return "已撤回完成请求。"
	case text == "Current Duo state.":
		return "当前 Duo 状态。"
	case text == "escalated to Goal mode successfully; session is now in PLAN phase":
		return "已成功切换到 Goal 模式；会话现处于 PLAN 阶段。"
	case strings.HasPrefix(text, "Fast mode has no phase sign-off."):
		return "Fast 模式没有阶段签署。Austin 请求验证后，Tony 使用 duo_set_verification 报告独立结论。"
	case text == "structured verification is only used in Fast mode":
		return "结构化验证仅用于 Fast 模式。"
	case text == "only Tony reports Fast-mode verification":
		return "Fast 模式下只有 Tony 可以报告验证结果。"
	case text == "verification can only be reported during VERIFY":
		return "只能在 VERIFY 阶段报告验证结果。"
	case strings.HasPrefix(text, "Only Austin can request Fast-mode verification"):
		return "Fast 模式下只有 Austin 可以请求验证。"
	default:
		return text
	}
}

func noteSuffix(note string) string {
	if note == "" {
		return ""
	}
	return " (note: " + note + ")"
}

func shortSHA(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 10 {
		return value[:10]
	}
	return value
}
