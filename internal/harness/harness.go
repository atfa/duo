package harness

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
)

type Sender interface {
	Send(context.Context, protocol.AgentID, protocol.Message) error
	IsConnected(protocol.AgentID) bool
}

type Config struct {
	IdleThreshold  time.Duration
	StallThreshold time.Duration
	Cooldown       time.Duration
	TickInterval   time.Duration
	// RecoveryGrace suppresses nudges for a while after a resumed session. Pi
	// needs time to reconnect and reload context, and that must not look like an
	// idle or stalled run.
	RecoveryGrace time.Duration
}

type Monitor struct {
	cfg     Config
	project *project.State
	tracker *Tracker
	sender  Sender
	bus     *events.Bus

	startedAt time.Time
	mu        sync.Mutex
	lastWake  time.Time

	// fast tracks one consecutive-idle episode so the Fast harness can ask Tony
	// for a diagnosis at most once before Austin shows real activity again.
	fast fastStuckEpisode
}

// fastStuckEpisode is the bookkeeping for one Fast stall episode. It resets on
// Austin activity or a phase change, so each new stall gets its own diagnosis.
type fastStuckEpisode struct {
	phase         project.Phase
	nudges        int
	diagnosisSent bool
	// lastAustin is the newest Austin LastActivity this episode has observed. The
	// Fast path never Touches Austin on its own nudge, so a newer value means real
	// agent activity, including a burst that started and settled between ticks.
	lastAustin time.Time
}

func NewMonitor(cfg Config, state *project.State, tracker *Tracker, sender Sender, bus *events.Bus) *Monitor {
	if cfg.TickInterval <= 0 {
		cfg.TickInterval = 2 * time.Second
	}
	return &Monitor{cfg: cfg, project: state, tracker: tracker, sender: sender, bus: bus, startedAt: time.Now()}
}

func (m *Monitor) Run(ctx context.Context) {
	ticker := time.NewTicker(m.cfg.TickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.maybeWake(ctx)
		}
	}
}

func (m *Monitor) maybeWake(ctx context.Context) {
	// A resumed session gets a grace period: neither agent has reported activity
	// yet, so an unguarded idle check would nudge immediately on startup.
	if m.cfg.RecoveryGrace > 0 && time.Since(m.startedAt) < m.cfg.RecoveryGrace {
		return
	}

	// Dispatch once, by mode. Fast nudges only the agent that must move next;
	// Goal keeps the original both-idle logic untouched below.
	if m.project.Snapshot().EffectiveMode() == project.ModeFast {
		m.maybeWakeFast(ctx)
		return
	}

	if !m.sender.IsConnected(protocol.Austin) || !m.sender.IsConnected(protocol.Tony) {
		return
	}

	state := m.project.Snapshot()
	if !state.Started || state.Phase == project.PhaseDone {
		return
	}

	// Once both agents have signed INTEGRATE, agent work is over: the remaining
	// step is Duo Core delivering the artifact to the user's repository. Nudging
	// Austin here would ask it to redo work that is already complete, whether
	// delivery is still pending or has just been handed back.
	if state.Phase == project.PhaseIntegrate && state.Ready[protocol.Austin] && state.Ready[protocol.Tony] {
		return
	}

	austin := m.tracker.Snapshot(protocol.Austin)
	tony := m.tracker.Snapshot(protocol.Tony)
	if austin.HumanAttached || tony.HumanAttached {
		return
	}

	latest := austin.LastActivity
	if tony.LastActivity.After(latest) {
		latest = tony.LastActivity
	}
	if state.LastMutation.After(latest) {
		latest = state.LastMutation
	}

	quietFor := time.Since(latest)
	bothIdle := !austin.Busy && !tony.Busy
	activeWork := austin.ProviderActive || tony.ProviderActive || austin.ToolDepth > 0 || tony.ToolDepth > 0
	normalIdle := bothIdle && !activeWork && quietFor >= m.cfg.IdleThreshold
	hardStalled := quietFor >= m.cfg.StallThreshold

	if !normalIdle && !hardStalled {
		return
	}

	m.mu.Lock()
	if !m.lastWake.IsZero() && time.Since(m.lastWake) < m.cfg.Cooldown {
		m.mu.Unlock()
		return
	}
	m.lastWake = time.Now()
	m.mu.Unlock()

	m.tracker.Touch(protocol.Austin)

	reason := "Both agents are idle while the Duo project is unfinished."
	if hardStalled && !normalIdle {
		reason = "The Duo project is unfinished and no activity has been observed for an unusually long time. This may be a stalled run."
	}

	prompt := fmt.Sprintf(
		"[Duo Harness]\n%s\n\n%s\n\nYou are the coordinator for recovery. Inspect the current phase and take the next concrete action. If your part is complete, call duo_set_status with ready=true. If Tony is the blocker, use duo_send to give Tony the exact information or nudge needed. In PLAN, create or revise the shared plan if necessary. Do not merely acknowledge this message.",
		reason,
		state.NudgeState(),
	)

	if m.bus != nil {
		m.bus.Emit(events.Event{Kind: events.KindHarness, Agent: protocol.Austin, Text: fmt.Sprintf("nudging Austin after %s without useful activity", quietFor.Round(time.Second))})
	}
	if err := m.sender.Send(ctx, protocol.Austin, protocol.Message{
		Version:   1,
		Type:      protocol.MsgHarnessPrompt,
		From:      protocol.Duo,
		To:        protocol.Austin,
		Text:      prompt,
		Timestamp: time.Now().UnixMilli(),
	}); err != nil && m.bus != nil {
		m.bus.Emit(events.Event{Kind: events.KindError, Agent: protocol.Duo, Text: "harness send failed: " + err.Error()})
	}
}

// maybeWakeFast nudges the one agent Fast actually needs to move next instead of
// waiting for both agents to be idle. In RUNNING that is Austin, so a thinking
// or idle Tony never blocks progress; in VERIFY it is Tony. A passed
// verification ends agent work: Duo Core is delivering, and neither agent should
// be asked to redo or re-verify anything.
func (m *Monitor) maybeWakeFast(ctx context.Context) {
	state := m.project.Snapshot()
	if !state.Started || state.Phase == project.PhaseDone {
		return
	}
	if state.Verification.Status == project.VerificationPassed {
		return
	}

	switch state.Phase {
	case project.PhaseRunning:
		m.fastNudgeAustin(ctx, state)
	case project.PhaseVerify:
		m.fastNudgeTony(ctx, state)
	}
}

// fastNudgeAustin nudges the driver when Austin itself is idle. Tony's state is
// deliberately irrelevant: Fast must not double-wait on two agents.
func (m *Monitor) fastNudgeAustin(ctx context.Context, state project.Snapshot) {
	if !m.sender.IsConnected(protocol.Austin) {
		return
	}
	austin := m.tracker.Snapshot(protocol.Austin)
	if austin.HumanAttached {
		return
	}

	// Real activity since the last observation starts a new episode, so a stall
	// that already got a diagnosis cannot be confused with the next one.
	if m.observeAustinActivity(project.PhaseRunning, austin.LastActivity) {
		return
	}

	active := austin.Busy || austin.ProviderActive || austin.ToolDepth > 0
	if active {
		m.resetFastEpisode()
		return
	}

	// A concrete agent failure is a deterministic reason to involve the copilot,
	// independent of idle timing. At most one diagnosis per episode.
	if austin.Failures > 0 && m.claimFastDiagnosis() {
		m.sendFastDiagnosis(ctx, state)
	}

	last := austin.LastActivity
	if state.LastMutation.After(last) {
		last = state.LastMutation
	}
	quietFor := time.Since(last)
	hardStalled := quietFor >= m.cfg.StallThreshold
	if quietFor < m.cfg.IdleThreshold && !hardStalled {
		return
	}

	nudges, allowed := m.beginFastNudge(project.PhaseRunning, false)
	if !allowed {
		return
	}

	reason := "Austin is idle while the Fast session is still in RUNNING."
	if hardStalled && quietFor < m.cfg.IdleThreshold {
		reason = "The Fast session is unfinished and Austin has been silent for an unusually long time."
	}
	prompt := fmt.Sprintf(
		"[Duo Harness]\n%s\n\nTony is only a copilot in Fast mode and does not block you: keep driving the task forward. Inspect the current state and take the next concrete action. When your work is genuinely complete, your worktree is clean and committed, and you have validated the result, request verification with duo_set_status ready=true. Do not merely acknowledge this message.\n\n%s",
		reason, state.NudgeState(),
	)

	// Two unsuccessful nudges is a stall, not just a pause. Ask the copilot once
	// per episode for an independent diagnosis; Tony's answer is advisory.
	if nudges >= 2 && m.claimFastDiagnosis() {
		m.sendFastDiagnosis(ctx, state)
	}

	if m.bus != nil {
		m.bus.Emit(events.Event{Kind: events.KindHarness, Agent: protocol.Austin, Text: fmt.Sprintf("nudging Austin after %s without useful activity", quietFor.Round(time.Second))})
	}
	m.sendHarnessPrompt(ctx, protocol.Austin, prompt)
}

// fastNudgeTony asks the verifier to move while a verification request is open.
func (m *Monitor) fastNudgeTony(ctx context.Context, state project.Snapshot) {
	if !m.sender.IsConnected(protocol.Tony) {
		return
	}
	tony := m.tracker.Snapshot(protocol.Tony)
	if tony.HumanAttached {
		return
	}

	last := tony.LastActivity
	if state.LastMutation.After(last) {
		last = state.LastMutation
	}
	quietFor := time.Since(last)
	active := tony.Busy || tony.ProviderActive || tony.ToolDepth > 0
	hardStalled := quietFor >= m.cfg.StallThreshold
	if active || (quietFor < m.cfg.IdleThreshold && !hardStalled) {
		return
	}
	if _, allowed := m.beginFastNudge(project.PhaseVerify, false); !allowed {
		return
	}

	prompt := fmt.Sprintf(
		"[Duo Harness]\nAustin requested independent verification of HEAD %s, but no verdict has arrived. Inspect that exact artifact yourself, then report result=passed, or result=issue_found with a concrete note, using duo_set_verification. Fast mode is single-writer: do not commit or edit the artifact. Do not merely acknowledge this message.\n\n%s",
		shortSHA(state.Verification.Head), state.NudgeState(),
	)
	if m.bus != nil {
		m.bus.Emit(events.Event{Kind: events.KindHarness, Agent: protocol.Tony, Text: fmt.Sprintf("nudging Tony to verify after %s without activity", quietFor.Round(time.Second))})
	}
	m.sendHarnessPrompt(ctx, protocol.Tony, prompt)
}

// sendFastDiagnosis asks Tony once per stuck episode for an independent
// diagnosis. It never takes over the work and never blocks Austin.
func (m *Monitor) sendFastDiagnosis(ctx context.Context, state project.Snapshot) {
	if !m.sender.IsConnected(protocol.Tony) {
		return
	}
	text := fmt.Sprintf(
		"[Duo Harness]\nAustin is not making progress in RUNNING (repeated idle nudges or a reported agent error). As copilot, give a concise diagnosis: the most likely blocker and the single next concrete step for Austin. Inspect the repository and state yourself first. This is advisory; do not take over the work or commit anything.\n\n%s",
		state.NudgeState(),
	)
	if m.bus != nil {
		m.bus.Emit(events.Event{Kind: events.KindHarness, Agent: protocol.Tony, Text: "asking Tony for a Fast stall diagnosis"})
	}
	m.sendHarnessPrompt(ctx, protocol.Tony, text)
}

func (m *Monitor) sendHarnessPrompt(ctx context.Context, agent protocol.AgentID, text string) {
	if err := m.sender.Send(ctx, agent, protocol.Message{
		Version:   protocol.Version,
		Type:      protocol.MsgHarnessPrompt,
		From:      protocol.Duo,
		To:        agent,
		Text:      text,
		Timestamp: time.Now().UnixMilli(),
	}); err != nil && m.bus != nil {
		m.bus.Emit(events.Event{Kind: events.KindError, Agent: protocol.Duo, Text: "harness send failed: " + err.Error()})
	}
}

// observeAustinActivity reports whether Austin did anything new since the last
// observation, starting a fresh stall episode if so. The Fast path never Touches
// Austin itself, so LastActivity only advances on real agent activity.
func (m *Monitor) observeAustinActivity(phase project.Phase, last time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fast.lastAustin.IsZero() {
		m.fast.lastAustin = last
		m.fast.phase = phase
		return false
	}
	if last.After(m.fast.lastAustin) {
		m.fast = fastStuckEpisode{phase: phase, lastAustin: last}
		return true
	}
	return false
}

// beginFastNudge serialises cooldown and episode bookkeeping for Fast nudges. A
// phase change or an explicit reset starts a fresh episode.
func (m *Monitor) beginFastNudge(phase project.Phase, reset bool) (int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if reset || m.fast.phase != phase {
		m.fast = fastStuckEpisode{phase: phase, lastAustin: m.fast.lastAustin}
	}
	if !m.lastWake.IsZero() && time.Since(m.lastWake) < m.cfg.Cooldown {
		return 0, false
	}
	m.lastWake = time.Now()
	m.fast.nudges++
	return m.fast.nudges, true
}

func (m *Monitor) resetFastEpisode() {
	m.mu.Lock()
	m.fast = fastStuckEpisode{}
	m.mu.Unlock()
}

func (m *Monitor) claimFastDiagnosis() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fast.diagnosisSent {
		return false
	}
	m.fast.diagnosisSent = true
	return true
}

func shortSHA(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "(unknown)"
	}
	if len(value) > 10 {
		return value[:10]
	}
	return value
}
