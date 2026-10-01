package coordinator

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/atfa/duo/internal/agent"
	"github.com/atfa/duo/internal/delivery"
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
	agents    *agent.Manager
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

	// Durable session state. EnableDurability must be called once, before the
	// event loop starts, after any resume has restored the project state.
	durable   snapshotStore
	journal   *sessionstore.EventLog
	log       *sessionstore.Logger
	compose   func(project.Snapshot) sessionstore.Snapshot
	persistMu sync.Mutex

	resumeWakeMu        sync.Mutex
	resumeWakeEnabled   bool
	resumeWakeAttempted map[protocol.AgentID]bool

	modelMu         sync.RWMutex
	currentThinking map[protocol.AgentID]string
	currentModel    map[protocol.AgentID]string
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
	return &Coordinator{server: server, project: state, tracker: tracker, workspace: ws, bus: bus}
}

// SetAgents connects the agent manager to the coordinator, allowing it to route
// prompts, nudges, and steers to non-TCP drivers (e.g. agy running in PTY).
func (c *Coordinator) SetAgents(agents *agent.Manager) {
	c.agents = agents
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
	if c.server != nil && c.server.IsConnected(agent) {
		return c.server.Send(ctx, agent, message)
	}
	if c.agents != nil {
		if d, ok := c.agents.Driver(agent); ok && d != nil && d.Running() {
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

// EnableResumeWake makes the next bridge connection for each agent receive one
// runtime-local resume prompt. Fresh sessions deliberately never enable this.
func (c *Coordinator) EnableResumeWake() {
	c.resumeWakeMu.Lock()
	c.resumeWakeEnabled = true
	c.resumeWakeAttempted = make(map[protocol.AgentID]bool)
	c.resumeWakeMu.Unlock()
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

// SubmitUserTask is the single human entry point used by the Duo TUI. Austin
// receives the task and is responsible for waking Tony through duo_send. On a
// DONE session it first reopens the round, so a composer follow-up is a normal
// task again instead of a dead end.
func (c *Coordinator) SubmitUserTask(ctx context.Context, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	if text == "/mode goal" || strings.HasPrefix(text, "/escalate") {
		reason := ""
		if strings.HasPrefix(text, "/escalate") {
			reason = strings.TrimSpace(strings.TrimPrefix(text, "/escalate"))
			if strings.HasPrefix(reason, ":") {
				reason = strings.TrimSpace(strings.TrimPrefix(reason, ":"))
			}
		}
		c.reopenFinishedSession()
		if err := c.EscalateToGoal(ctx, reason); err != nil {
			c.emit(events.KindSystem, protocol.Duo, "", fmt.Sprintf("cannot escalate: %v", err))
			return err
		}
		return nil
	}
	if text == "/mode fast" {
		err := fmt.Errorf("cannot switch to Fast mode: sessions can only escalate from Fast to Goal")
		c.emit(events.KindSystem, protocol.Duo, "", err.Error())
		return err
	}

	if !c.IsAgentConnected(protocol.Austin) {
		return fmt.Errorf("Austin is not connected yet")
	}
	c.reopenFinishedSession()
	c.project.MarkStarted()
	c.tracker.Touch(protocol.Austin)
	c.emit(events.KindUser, protocol.Duo, protocol.Austin, text)
	promptText := "[Human task from Duo]\n\n" + text
	if c.project.Snapshot().EffectiveMode() == project.ModeFast {
		promptText += "\n\n(Duo guidance: Once your work is committed and your worktree is clean, request verification with duo_set_status ready=true so Tony can independently verify.)"
	}
	return c.sendToAgent(ctx, protocol.Austin, protocol.Message{
		Version: 1, Type: protocol.MsgHumanPrompt, From: protocol.Duo, To: protocol.Austin,
		Text: promptText, Timestamp: time.Now().UnixMilli(),
	})
}

// EscalateToGoal dynamically escalates an active Fast mode session to Goal mode.
// It switches mode to Goal and phase to PLAN, creates a shared plan v1 seeded
// with the escalation reason, invalidates approvals, persists the updated state,
// and notifies both agents of their updated roles.
func (c *Coordinator) EscalateToGoal(ctx context.Context, reason string) error {
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

	reasonDesc := "(none provided)"
	if reason != "" {
		reasonDesc = reason
	}

	austinNotice := fmt.Sprintf(
		"[Duo Mode Escalation]\nThe session has escalated from Fast mode to Goal mode.\nReason: %s\nPhase: PLAN (shared plan v1 initialized).\n\nFast-mode single-driver workflow has switched to Goal-mode collaborative workflow:\n- Both Austin and Tony must co-design the shared plan.\n- Refine the shared plan using duo_set_plan.\n- When aligned, both Austin and Tony must approve with duo_set_status(ready=true) to advance to EXECUTE.\n- Existing commits in your worktree are preserved.",
		reasonDesc,
	)
	tonyNotice := fmt.Sprintf(
		"[Duo Mode Escalation]\nThe session has escalated from Fast mode to Goal mode.\nReason: %s\nPhase: PLAN (shared plan v1 initialized).\n\nYou are now an active co-designer and co-developer (no longer read-only):\n- Read current plan with duo_status and collaborate with Austin via duo_send.\n- Refine the shared plan using duo_set_plan.\n- When aligned, both Austin and Tony must approve with duo_set_status(ready=true) to advance to EXECUTE.\n- In EXECUTE, work independently in your own worktree.",
		reasonDesc,
	)

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

	if c.tracker != nil {
		c.tracker.Touch(protocol.Austin)
		c.tracker.Touch(protocol.Tony)
	}

	return nil
}

// reopenFinishedSession turns a DONE session back into an active round when the
// human submits a new task through the Duo composer. Native Pi attachment
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
	c.tracker.Touch(client.Agent)
	c.recordEvent("bridge_connect", map[string]any{"agent": string(client.Agent)})
	c.emit(events.KindSystem, client.Agent, "", fmt.Sprintf("%s connected", client.Agent))
	c.wakeResumedAgent(ctx, client.Agent)
}

func (c *Coordinator) wakeResumedAgent(ctx context.Context, agent protocol.AgentID) {
	c.resumeWakeMu.Lock()
	if !c.resumeWakeEnabled || c.resumeWakeAttempted[agent] {
		c.resumeWakeMu.Unlock()
		return
	}
	c.resumeWakeAttempted[agent] = true
	c.resumeWakeMu.Unlock()

	snap := c.project.Snapshot()
	if err := c.sendToAgent(ctx, agent, protocol.Message{
		Version: protocol.Version, Type: protocol.MsgResumePrompt, From: protocol.Duo, To: agent,
		Text: c.resumePrompt(agent, snap), Timestamp: time.Now().UnixMilli(),
	}); err != nil {
		c.recordEvent("resume_wake_failed", map[string]any{"agent": string(agent), "phase": string(snap.Phase), "planVersion": snap.PlanVersion, "error": err.Error()})
		c.logf("resume wake for %s failed: %v", agent, err)
		c.emit(events.KindError, protocol.Duo, agent, fmt.Sprintf("resume wake for %s failed: %v", agent, err))
		return
	}
	c.tracker.Touch(agent)
	c.recordEvent("resume_wake_sent", map[string]any{"agent": string(agent), "phase": string(snap.Phase), "planVersion": snap.PlanVersion})
	c.logf("sent resume wake to %s (phase=%s planVersion=%d)", agent, snap.Phase, snap.PlanVersion)
	c.emit(events.KindSystem, protocol.Duo, agent, fmt.Sprintf("sent resume wake to %s", agent))
}

func (c *Coordinator) OnDisconnect(client *transport.Client) {
	c.tracker.Reset(client.Agent)
	c.recordEvent("bridge_disconnect", map[string]any{"agent": string(client.Agent)})
	c.emit(events.KindSystem, client.Agent, "", fmt.Sprintf("%s disconnected", client.Agent))
}

func (c *Coordinator) OnMessage(ctx context.Context, client *transport.Client, message protocol.Message) {
	switch message.Type {
	case protocol.MsgTest:
		c.emit(events.KindSystem, client.Agent, "", "TEST: "+message.Text)

	case protocol.MsgActivity:
		c.handleActivity(client.Agent, message)

	case protocol.MsgAssistantMessage:
		c.handleAssistant(client.Agent, message)

	case protocol.MsgAgentError:
		c.handleAgentError(client.Agent, message)

	case protocol.MsgPeerMessage:
		c.handlePeerMessage(ctx, client, message)

	case protocol.MsgSetPlan:
		c.handleSetPlan(ctx, client, message)

	case protocol.MsgSetStatus:
		c.handleSetStatus(ctx, client, message)

	case protocol.MsgSetVerification:
		c.handleSetVerification(ctx, client, message)

	case protocol.MsgEscalate:
		c.handleEscalate(ctx, client, message)

	case protocol.MsgGetStatus:
		c.handleGetStatus(ctx, client, message)

	case protocol.MsgModelState:
		c.handleModelState(client.Agent, message)

	case protocol.MsgThinkingState:
		c.handleThinkingState(client.Agent, message)

	default:
		if message.RequestID != "" {
			_ = c.respond(ctx, client, message.RequestID, false, "unknown message type: "+string(message.Type), "")
		}
	}
}

func (c *Coordinator) handleActivity(agent protocol.AgentID, message protocol.Message) {
	c.tracker.Handle(agent, message.Activity)
	c.tracker.Note(agent, message.Activity, message.Tool, message.Detail)
	if message.Activity == protocol.ActivityAgentStart {
		c.project.MarkStarted()
	}
	if c.bus != nil {
		c.bus.Emit(events.Event{Kind: events.KindActivity, Agent: agent})
	}
}

// RecordActivity records agent activity observations (used by observation-based drivers like agy).
func (c *Coordinator) RecordActivity(agent protocol.AgentID, message protocol.Message) {
	if message.Type == protocol.MsgAssistantMessage {
		c.handleAssistant(agent, message)
		return
	}
	c.handleActivity(agent, message)
}

func (c *Coordinator) handleAssistant(agent protocol.AgentID, message protocol.Message) {
	if strings.TrimSpace(message.Text) == "" {
		return
	}
	c.tracker.Touch(agent)
	c.emit(events.KindAssistant, agent, "", message.Text)
}

// SetModel asks one agent to switch its active model. If connected via the transport
// server (e.g. Pi bridge), it forwards the message. If not connected (e.g. Agy driver),
// it records the state locally and emits the model event to the bus.
func (c *Coordinator) SetModel(ctx context.Context, agent protocol.AgentID, provider, id string) error {
	provider, id = strings.TrimSpace(provider), strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("set model: model id is required")
	}
	if c.server != nil && c.server.IsConnected(agent) {
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

// CycleThinking advances one agent's thinking level. When connected to a bridge
// (e.g. Pi), it delegates to the bridge. Otherwise (e.g. Agy), it cycles through
// standard levels locally.
func (c *Coordinator) CycleThinking(ctx context.Context, agent protocol.AgentID) error {
	if c.server != nil && c.server.IsConnected(agent) {
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
	if provider != "" && provider != "agy" && !strings.HasPrefix(model, provider+"/") {
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
	from := client.Agent
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
	snap, err := c.project.SetPlan(client.Agent, message.Plan)
	if err != nil {
		_ = c.respond(ctx, client, message.RequestID, false, err.Error(), c.statusText(ctx))
		return
	}

	c.tracker.Touch(client.Agent)
	c.recordEvent("plan_updated", map[string]any{
		"agent":       string(client.Agent),
		"planVersion": snap.PlanVersion,
	})
	c.emit(events.KindSystem, client.Agent, "", fmt.Sprintf("updated shared plan → v%d; both signatures reset", snap.PlanVersion))
	_ = c.respond(ctx, client, message.RequestID, true,
		fmt.Sprintf("Shared plan updated to v%d. Both signatures were reset.", snap.PlanVersion), c.statusText(ctx))

	peer := protocol.PeerOf(client.Agent)
	if peer == "" {
		return
	}

	notice := fmt.Sprintf(
		"[Duo plan update]\n%s updated the shared plan to v%d.\n\n%s\n\nReview this exact version. Discuss concerns with duo_send. If you approve it, call duo_set_status with ready=true. Updating the plan again invalidates both signatures. Exploratory edits in your own worktree are allowed during PLAN, but they remain provisional until the plan is jointly approved.",
		client.Agent, snap.PlanVersion, snap.Plan,
	)
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
	if c.project.Snapshot().EffectiveMode() == project.ModeFast {
		c.handleFastSetStatus(ctx, client, message)
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
		evidence, err = c.evidenceForReady(ctx, client.Agent, snapBefore.Phase, snapBefore)
		if err != nil {
			_ = c.respond(ctx, client, message.RequestID, false, err.Error(), c.statusText(ctx))
			return
		}
	}

	snap, tr, err := c.project.SetReady(client.Agent, *message.Ready, message.Note, evidence)
	if err != nil {
		_ = c.respond(ctx, client, message.RequestID, false, err.Error(), c.statusText(ctx))
		return
	}

	c.tracker.Touch(client.Agent)
	peer := protocol.PeerOf(client.Agent)

	// A signature is an assertion about a specific artifact in a specific phase;
	// record exactly what was signed so the journal can be audited.
	if *message.Ready {
		c.recordEvent("signature", map[string]any{
			"agent":    string(client.Agent),
			"phase":    string(snap.Phase),
			"evidence": evidence,
		})
	} else {
		c.recordEvent("signature_revoked", map[string]any{
			"agent": string(client.Agent),
			"phase": string(snap.Phase),
			"note":  message.Note,
		})
	}

	if tr.ReadyForDelivery {
		if c.TestCommand() != "" && c.workspace != nil {
			if testErr := c.runTestGate(ctx); testErr != nil {
				c.project.RevokeReady(client.Agent, "automated test gate failed")
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
			fmt.Sprintf("%s signed %s. Waiting for %s.", client.Agent, snap.Phase, peer), c.statusText(ctx))
	} else {
		_ = c.respond(ctx, client, message.RequestID, true,
			fmt.Sprintf("%s marked %s as not ready.", client.Agent, snap.Phase), c.statusText(ctx))
	}

	if peer == "" {
		return
	}

	var notice string
	if *message.Ready {
		notice = fmt.Sprintf(
			"[Duo status]\n%s has signed %s. Your current status is ready=%v. If your work for this phase is genuinely complete and no unresolved issue remains, call duo_set_status with ready=true. Otherwise continue working or discuss via duo_send.",
			client.Agent, snap.Phase, snap.Ready[peer],
		)
	} else {
		notice = fmt.Sprintf(
			"[Duo status]\n%s revoked readiness for %s%s. Continue collaboration until the issue is resolved.",
			client.Agent, snap.Phase, noteSuffix(message.Note),
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
	if client.Agent != protocol.Austin {
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

	snap, tr, err := c.project.SetReady(client.Agent, *message.Ready, message.Note, evidence)
	if err != nil {
		_ = c.respond(ctx, client, message.RequestID, false, err.Error(), c.statusText(ctx))
		return
	}
	c.tracker.Touch(client.Agent)

	if !*message.Ready {
		c.recordEvent("completion_withdrawn", map[string]any{"phase": string(snap.Phase)})
		_ = c.respond(ctx, client, message.RequestID, true, "Completion request withdrawn.", c.statusText(ctx))
		c.sendFastNotice(ctx, protocol.Tony, "[Duo status]\nAustin withdrew the verification request. Stand by; no verification is pending.")
		return
	}

	c.recordEvent("verification_requested", map[string]any{"head": snap.Verification.Head})
	if tr.Advanced {
		c.recordEvent("phase_transition", map[string]any{"from": string(tr.Previous), "to": string(tr.Next)})
		c.logf("[FAST] phase advanced %s → %s", tr.Previous, tr.Next)
		c.emit(events.KindSystem, protocol.Duo, "", fmt.Sprintf("verification requested → %s", tr.Next))
	}
	_ = c.respond(ctx, client, message.RequestID, true,
		fmt.Sprintf("Verification requested for Austin HEAD %s. Tony must independently verify before Duo delivers.", shortSHA(snap.Verification.Head)),
		c.statusText(ctx))
	c.sendFastNotice(ctx, protocol.Tony, fmt.Sprintf(
		"[Duo verification request]\nAustin reports the work complete and requested independent verification of HEAD %s.\n\nInspect that exact artifact yourself. Then report a structured verdict with duo_set_verification: result=passed, or result=issue_found with a concrete note describing the problem. Fast mode is single-writer: do not commit or edit the artifact; if a fix is needed, report it so Austin applies it.",
		shortSHA(snap.Verification.Head),
	))
}

// reopenFastRoundForNewWork detects a Fast round that was driven outside the Duo
// composer. The composer reopens a DONE session through SubmitUserTask, but the
// human can also talk to Austin in native Pi (Ctrl+A) and let it commit; that
// path never reaches Duo, so Austin's next completion request is the only signal
// of the new round. A request that names a different Austin HEAD than the
// delivered round is genuine new work: reopen the finished round and let the
// caller handle it as an ordinary RUNNING → VERIFY request. A retry that still
// names the delivered HEAD is a duplicate of the finished round and stays
// rejected, so a stray resend can never undo a delivery.
func (c *Coordinator) reopenFastRoundForNewWork(ctx context.Context, client *transport.Client, message protocol.Message) bool {
	if message.Ready == nil || !*message.Ready || client.Agent != protocol.Austin {
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
	if c.project.Snapshot().EffectiveMode() != project.ModeFast {
		_ = c.respond(ctx, client, message.RequestID, false,
			"structured verification is only used in Fast mode", c.statusText(ctx))
		return
	}
	if client.Agent != protocol.Tony {
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

	snap, tr, err := c.project.SetVerification(client.Agent, result, message.Note, target)
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
	if err := c.EscalateToGoal(ctx, reason); err != nil {
		_ = c.respond(ctx, client, message.RequestID, false, err.Error(), c.statusText(ctx))
		return
	}
	_ = c.respond(ctx, client, message.RequestID, true, "escalated to Goal mode successfully; session is now in PLAN phase", c.statusText(ctx))
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
	if currentDelivery.Applied() {
		if snap.Phase == project.PhaseIntegrate || snap.Phase == project.PhaseVerify {
			if _, err := c.project.Complete(); err != nil {
				c.logf("complete applied delivery: %v", err)
				return
			}
			if err := c.persistStrict("project complete"); err != nil {
				c.logf("persist completed applied delivery: %v", err)
			}
		}
		return
	}

	// The work must be complete and every approval must still describe the
	// artifact about to be delivered. Fast has one gate (a verification bound to
	// Austin's HEAD); Goal has another (dual INTEGRATE sign-off). Everything after
	// this point is the same idempotent delivery transaction for both modes.
	fast := snap.EffectiveMode() == project.ModeFast
	if fast {
		if snap.Phase != project.PhaseVerify {
			return
		}
	} else if snap.Phase != project.PhaseIntegrate || !snap.Ready[protocol.Austin] || !snap.Ready[protocol.Tony] {
		return
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

	if fast {
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
	} else {
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

	// The phase the work was approved in is INTEGRATE in Goal and VERIFY in
	// Fast; capture it before Complete so the DONE notice names the real edge.
	previous := c.project.Snapshot().Phase
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
		c.emit(events.KindSystem, protocol.Duo, "", "integration conflict in "+result.AustinPath)
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
	c.tracker.Touch(client.Agent)
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
