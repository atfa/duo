package coordinator

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

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
	project   *project.State
	tracker   *harness.Tracker
	workspace workspace.Manager
	bus       *events.Bus

	integrationMu sync.RWMutex
	integration   workspace.IntegrationResult

	// Durable session state. EnableDurability must be called once, before the
	// event loop starts, after any resume has restored the project state.
	durable   *sessionstore.Store
	journal   *sessionstore.EventLog
	log       *sessionstore.Logger
	compose   func(project.Snapshot) sessionstore.Snapshot
	persistMu sync.Mutex
}

// Durability wires a session store into the coordinator.
type Durability struct {
	Store   *sessionstore.Store
	Events  *sessionstore.EventLog
	Log     *sessionstore.Logger
	Compose func(project.Snapshot) sessionstore.Snapshot
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
	if c.durable == nil || c.compose == nil {
		return
	}
	c.persistMu.Lock()
	defer c.persistMu.Unlock()
	if err := c.durable.Save(c.compose(c.project.Snapshot())); err != nil {
		if c.log != nil {
			c.log.Printf("failed to persist session state after %s: %v", reason, err)
		}
		c.emit(events.KindError, protocol.Duo, "", "failed to persist session state: "+err.Error())
	}
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

// SubmitUserTask is the single human entry point used by the Duo TUI.
// Austin receives the task and is responsible for waking Tony through duo_send.
func (c *Coordinator) SubmitUserTask(ctx context.Context, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if !c.server.IsConnected(protocol.Austin) {
		return fmt.Errorf("Austin is not connected yet")
	}
	c.project.MarkStarted()
	c.tracker.Touch(protocol.Austin)
	c.emit(events.KindUser, protocol.Duo, protocol.Austin, text)
	return c.server.Send(ctx, protocol.Austin, protocol.Message{
		Version: 1, Type: protocol.MsgHumanPrompt, From: protocol.Duo, To: protocol.Austin,
		Text: "[Human task from Duo]\n\n" + text, Timestamp: time.Now().UnixMilli(),
	})
}

func (c *Coordinator) StatusText(ctx context.Context) string { return c.statusText(ctx) }

func (c *Coordinator) OnConnect(_ context.Context, client *transport.Client) {
	c.tracker.Touch(client.Agent)
	c.recordEvent("bridge_connect", map[string]any{"agent": string(client.Agent)})
	c.emit(events.KindSystem, client.Agent, "", fmt.Sprintf("%s connected", client.Agent))
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

	case protocol.MsgGetStatus:
		c.handleGetStatus(ctx, client, message)

	default:
		if message.RequestID != "" {
			_ = c.respond(ctx, client, message.RequestID, false, "unknown message type: "+string(message.Type), "")
		}
	}
}

func (c *Coordinator) handleActivity(agent protocol.AgentID, message protocol.Message) {
	c.tracker.Handle(agent, message.Activity)
	if message.Activity == protocol.ActivityAgentStart {
		c.project.MarkStarted()
	}
}

func (c *Coordinator) handleAssistant(agent protocol.AgentID, message protocol.Message) {
	if strings.TrimSpace(message.Text) == "" {
		return
	}
	c.tracker.Touch(agent)
	c.emit(events.KindAssistant, agent, "", message.Text)
}

func (c *Coordinator) handleAgentError(agent protocol.AgentID, message protocol.Message) {
	if strings.TrimSpace(message.Text) == "" {
		return
	}
	c.tracker.Touch(agent)
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

	err := c.server.Send(ctx, to, protocol.Message{
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
	_ = c.server.Send(ctx, peer, protocol.Message{
		Version:   1,
		Type:      protocol.MsgDuoNotice,
		From:      protocol.Duo,
		To:        peer,
		Text:      notice,
		Timestamp: time.Now().UnixMilli(),
	})
}

func (c *Coordinator) handleSetStatus(ctx context.Context, client *transport.Client, message protocol.Message) {
	if message.Ready == nil {
		_ = c.respond(ctx, client, message.RequestID, false, "ready must be true or false", c.statusText(ctx))
		return
	}

	snapBefore := c.project.Snapshot()
	evidence := ""

	if *message.Ready {
		if err := c.invalidateStaleApprovals(ctx, snapBefore); err != nil {
			_ = c.respond(ctx, client, message.RequestID, false, err.Error(), c.statusText(ctx))
			return
		}
		// Re-read because stale signatures may have been revoked.
		snapBefore = c.project.Snapshot()
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
	_ = c.server.Send(ctx, peer, protocol.Message{
		Version:   1,
		Type:      protocol.MsgDuoNotice,
		From:      protocol.Duo,
		To:        peer,
		Text:      notice,
		Timestamp: time.Now().UnixMilli(),
	})
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
	parts := []string{c.project.Snapshot().String(), c.workspace.Set().String()}
	for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		if status, err := c.workspace.Status(ctx, agent); err == nil {
			parts = append(parts, status.String())
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
