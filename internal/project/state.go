package project

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/atfa/duo/internal/protocol"
)

var (
	ErrWrongPhase   = errors.New("operation is not allowed in the current phase")
	ErrMissingPlan  = errors.New("cannot approve PLAN before a shared plan exists")
	ErrMissingIssue = errors.New("issue_found requires a concrete issue description")
	ErrModeMismatch = errors.New("operation is not available in this Duo mode")
	ErrProjectDone  = errors.New("project is already DONE")
	ErrAlreadyGoal  = errors.New("project is already in Goal mode")
	ErrUnknownAgent = errors.New("unknown agent")
)

type Snapshot struct {
	Mode         Mode
	Phase        Phase
	Plan         string
	PlanVersion  int
	Ready        map[protocol.AgentID]bool
	Notes        map[protocol.AgentID]string
	Evidence     map[protocol.AgentID]string
	Verification Verification
	Started      bool
	LastMutation time.Time

	// Steps is the Step-mode queue and Step is its 1-based cursor: the index of the
	// step being worked, and len(Steps)+1 once every step has landed. Duo owns both,
	// so they are additive state and the persisted schema does not change.
	Step  int   `json:"step"`
	Steps Steps `json:"steps"`
}

// EffectiveMode normalizes a possibly-zero mode through the one rule in
// Mode.Normalize, so a Step session can never be read as Goal.
func (s Snapshot) EffectiveMode() Mode {
	return s.Mode.Normalize()
}

func (s Snapshot) String() string {
	switch s.EffectiveMode() {
	case ModeFast:
		return s.fastString()
	case ModeStep:
		return s.stepString()
	}
	return s.goalString()
}

// stepString is the shared-state rendering plus the queue: the phase and
// signature block is the same information, and the queue (including each item's
// Fast/Goal policy) is what tells an agent which step it is working and which
// gate applies. The full queue is shown here because this rendering is printed
// once per request, not on a timer.
func (s Snapshot) stepString() string {
	out := s.goalString()
	if len(s.Steps) == 0 {
		return out + "\n\nStep queue: none set — either agent sets it with duo_set_steps before the session can leave PLAN."
	}
	return out + "\n\nStep queue (" + fmt.Sprintf("%d step(s), cursor at %d", len(s.Steps), s.Step) + "):\n" + s.Steps.Render()
}

// NudgeState is the compact state block injected into harness nudges. Unlike
// String it never inlines the shared plan body: a nudge repeats on a timer, and
// re-sending a multi-KB plan every time is pure token cost. The agent already
// has the plan in its own Pi context and can re-read it with duo_status, so the
// nudge only has to point at it. Everything else (phase, signatures, notes and
// evidence) is kept, so the nudge carries the same signal as before.
func (s Snapshot) NudgeState() string {
	if s.EffectiveMode() == ModeFast {
		return s.fastString()
	}

	planRef := "(none yet)"
	if plan := strings.TrimSpace(s.Plan); plan != "" {
		planRef = fmt.Sprintf("v%d, %d bytes — read it with duo_status", s.PlanVersion, len(plan))
	}

	nudge := fmt.Sprintf(
		"Duo state:\n- phase: %s\n- plan version: %d\n- Austin ready: %t%s%s\n- Tony ready: %t%s%s\n- shared plan: %s",
		s.Phase,
		s.PlanVersion,
		s.Ready[protocol.Austin], optionalNote(s.Notes[protocol.Austin]), optionalEvidence(s.Evidence[protocol.Austin]),
		s.Ready[protocol.Tony], optionalNote(s.Notes[protocol.Tony]), optionalEvidence(s.Evidence[protocol.Tony]),
		planRef,
	)
	// A nudge repeats on a timer, so the queue is reduced to the cursor line plus
	// one short line per remaining step. Step bodies never appear here: over a
	// ten-step loop they would be the dominant token cost of the session.
	if s.EffectiveMode() == ModeStep && len(s.Steps) > 0 {
		nudge += "\n- step queue:\n" + s.Steps.RenderCursor()
	}
	return nudge
}

// goalString is byte-for-byte the historical rendering: it is what every
// pre-Fast test, tool response and user reasonably expects in Goal mode.
func (s Snapshot) goalString() string {
	plan := strings.TrimSpace(s.Plan)
	if plan == "" {
		plan = "(none yet)"
	}

	return fmt.Sprintf(
		"Duo state:\n- phase: %s\n- plan version: %d\n- Austin ready: %t%s%s\n- Tony ready: %t%s%s\n- shared plan:\n%s",
		s.Phase,
		s.PlanVersion,
		s.Ready[protocol.Austin], optionalNote(s.Notes[protocol.Austin]), optionalEvidence(s.Evidence[protocol.Austin]),
		s.Ready[protocol.Tony], optionalNote(s.Notes[protocol.Tony]), optionalEvidence(s.Evidence[protocol.Tony]),
		plan,
	)
}

// fastString reports the Fast workflow instead of plan/sign-off noise.
func (s Snapshot) fastString() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Duo state (FAST):\n- phase: %s\n- driver: Austin\n- copilot/verifier: Tony\n- verification: %s", s.Phase, s.Verification.Label())
	if head := shortHash(s.Verification.Head); head != "(none)" {
		fmt.Fprintf(&b, " (target: %s)", head)
	}
	if note := strings.TrimSpace(s.Verification.Note); note != "" {
		fmt.Fprintf(&b, "\n- verification note: %s", note)
	}
	if note := strings.TrimSpace(s.Notes[protocol.Austin]); note != "" {
		fmt.Fprintf(&b, "\n- Austin note: %s", note)
	}
	return b.String()
}

func optionalNote(note string) string {
	note = strings.TrimSpace(note)
	if note == "" {
		return ""
	}
	return " (note: " + note + ")"
}

func optionalEvidence(evidence string) string {
	evidence = strings.TrimSpace(evidence)
	if evidence == "" {
		return ""
	}
	if len(evidence) > 12 {
		evidence = evidence[:12]
	}
	return " (evidence: " + evidence + ")"
}

type Transition struct {
	Advanced bool
	Previous Phase
	Next     Phase
	// ReadyForDelivery is set when the work is complete and the final artifact
	// can be handed back to the user's repository. In Goal it is dual sign-off on
	// INTEGRATE; in Fast it is Tony's passed verification. Either way final
	// approval is an edge, not a level: retries of an already accepted status
	// must not start a second delivery transaction.
	ReadyForDelivery bool
}

type State struct {
	mu sync.RWMutex

	mode         Mode
	phase        Phase
	plan         string
	planVersion  int
	step         int
	steps        Steps
	ready        map[protocol.AgentID]bool
	notes        map[protocol.AgentID]string
	evidence     map[protocol.AgentID]string
	verification Verification
	started      bool
	lastMutation time.Time

	// onChange is invoked with the latest snapshot after every mutation. It is
	// called outside the lock; the state itself never touches disk.
	onChange func(Snapshot)
}

// NewState returns a Goal session. It is the historical constructor and stays
// Goal so existing callers and tests are unaffected; new sessions ask for a
// mode explicitly through NewStateFor.
func NewState() *State { return NewStateFor(ModeGoal) }

// NewStateFor returns a fresh session in the requested mode. Anything that is
// not a mode this binary knows is treated as Goal, so a zero value behaves like a
// legacy session.
func NewStateFor(mode Mode) *State {
	mode = mode.Normalize()
	return &State{
		mode:  mode,
		phase: mode.initialPhase(),
		ready: map[protocol.AgentID]bool{
			protocol.Austin: false,
			protocol.Tony:   false,
		},
		notes: map[protocol.AgentID]string{
			protocol.Austin: "",
			protocol.Tony:   "",
		},
		evidence: map[protocol.AgentID]string{
			protocol.Austin: "",
			protocol.Tony:   "",
		},
		lastMutation: time.Now(),
	}
}

// Mode reports the session mode.
func (s *State) Mode() Mode {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mode
}

func (s *State) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshotLocked()
}

func (s *State) snapshotLocked() Snapshot {
	return Snapshot{
		Mode:  s.mode,
		Phase: s.phase,
		Plan:  s.plan,
		Ready: map[protocol.AgentID]bool{
			protocol.Austin: s.ready[protocol.Austin],
			protocol.Tony:   s.ready[protocol.Tony],
		},
		Notes: map[protocol.AgentID]string{
			protocol.Austin: s.notes[protocol.Austin],
			protocol.Tony:   s.notes[protocol.Tony],
		},
		Evidence: map[protocol.AgentID]string{
			protocol.Austin: s.evidence[protocol.Austin],
			protocol.Tony:   s.evidence[protocol.Tony],
		},
		PlanVersion:  s.planVersion,
		Step:         s.step,
		Steps:        append(Steps(nil), s.steps...),
		Verification: s.verification,
		Started:      s.started,
		LastMutation: s.lastMutation,
	}
}

func (s *State) MarkStarted() {
	s.mu.Lock()
	s.started = true
	snap, hook := s.snapshotLocked(), s.onChange
	s.mu.Unlock()
	notify(hook, snap)
}

func (s *State) SetPlan(agent protocol.AgentID, plan string) (Snapshot, error) {
	if agent != protocol.Austin && agent != protocol.Tony {
		return Snapshot{}, ErrUnknownAgent
	}
	plan = strings.TrimSpace(plan)
	if plan == "" {
		return Snapshot{}, errors.New("plan must not be empty")
	}

	s.mu.Lock()
	if err := s.workflow().setPlan(s, plan); err != nil {
		snap := s.snapshotLocked()
		s.mu.Unlock()
		return snap, err
	}
	snap, hook := s.snapshotLocked(), s.onChange
	s.mu.Unlock()
	notify(hook, snap)
	return snap, nil
}

// SetSteps sets the Step-mode queue from a Markdown checkbox list, or appends to
// it. Duo parses the list once here, so the array is the only source of truth and
// no agent can move the cursor by editing text.
func (s *State) SetSteps(agent protocol.AgentID, list string, appendOnly bool) (Snapshot, error) {
	if agent != protocol.Austin && agent != protocol.Tony {
		return Snapshot{}, ErrUnknownAgent
	}
	steps := ParseSteps(list)

	s.mu.Lock()
	if err := s.workflow().setSteps(s, steps, appendOnly); err != nil {
		snap := s.snapshotLocked()
		s.mu.Unlock()
		return snap, err
	}
	snap, hook := s.snapshotLocked(), s.onChange
	s.mu.Unlock()
	notify(hook, snap)
	return snap, nil
}

// LandStep records that the current step reached the user's repository and lets
// the mode decide what that means: STEP returns to EXECUTE for the next step,
// every other mode refuses.
func (s *State) LandStep(head string) (Snapshot, Transition, error) {
	s.mu.Lock()
	previous := s.phase
	transition, err := s.workflow().landStep(s, strings.TrimSpace(head))
	if err != nil {
		snap := s.snapshotLocked()
		s.mu.Unlock()
		return snap, Transition{}, err
	}
	transition.Previous = previous
	transition.Next = s.phase
	transition.Advanced = s.phase != previous
	snap, hook := s.snapshotLocked(), s.onChange
	s.mu.Unlock()
	notify(hook, snap)
	return snap, transition, nil
}

// RevokeStepSubmission withdraws the current step's submission and review and
// returns the session to EXECUTE. It is what delivery calls when the reviewed HEAD
// is no longer the artifact under the cursor: the verdict described something else,
// so landing it would put unverified work in the user's repository.
func (s *State) RevokeStepSubmission(reason string) (Snapshot, Transition, error) {
	s.mu.Lock()
	previous := s.phase
	transition, err := s.workflow().revokeStep(s, reason)
	if err != nil {
		snap := s.snapshotLocked()
		s.mu.Unlock()
		return snap, Transition{}, err
	}
	transition.Previous = previous
	transition.Next = s.phase
	transition.Advanced = s.phase != previous
	snap, hook := s.snapshotLocked(), s.onChange
	s.mu.Unlock()
	notify(hook, snap)
	return snap, transition, nil
}

func (s *State) SetReady(agent protocol.AgentID, ready bool, note, evidence string) (Snapshot, Transition, error) {
	if agent != protocol.Austin && agent != protocol.Tony {
		return Snapshot{}, Transition{}, ErrUnknownAgent
	}

	s.mu.Lock()
	previous := s.phase
	transition, err := s.workflow().setReady(s, agent, ready, note, evidence)
	if err != nil {
		snap := s.snapshotLocked()
		s.mu.Unlock()
		return snap, Transition{}, err
	}
	transition.Previous = previous
	transition.Next = s.phase
	transition.Advanced = s.phase != previous

	snap, hook := s.snapshotLocked(), s.onChange
	s.mu.Unlock()
	notify(hook, snap)
	return snap, transition, nil
}

// SetVerification records a structured verification verdict. The mode policy
// decides who may report it, in which phase, and whether it completes the work.
func (s *State) SetVerification(agent protocol.AgentID, result VerificationResult, note, head string) (Snapshot, Transition, error) {
	if agent != protocol.Austin && agent != protocol.Tony {
		return Snapshot{}, Transition{}, ErrUnknownAgent
	}

	s.mu.Lock()
	previous := s.phase
	transition, err := s.workflow().setVerification(s, agent, result, note, head)
	if err != nil {
		snap := s.snapshotLocked()
		s.mu.Unlock()
		return snap, Transition{}, err
	}
	transition.Previous = previous
	transition.Next = s.phase
	transition.Advanced = s.phase != previous

	snap, hook := s.snapshotLocked(), s.onChange
	s.mu.Unlock()
	notify(hook, snap)
	return snap, transition, nil
}

// Complete performs the final work → DONE transition after the artifact has
// been handed back to the user's repository.
func (s *State) Complete() (Snapshot, error) {
	s.mu.Lock()
	if err := s.workflow().complete(s); err != nil {
		snap := s.snapshotLocked()
		s.mu.Unlock()
		return snap, err
	}
	snap, hook := s.snapshotLocked(), s.onChange
	s.mu.Unlock()
	notify(hook, snap)
	return snap, nil
}

// Reopen starts a new round after DONE so a follow-up human task can be worked
// and verified again. It only succeeds from DONE; the mode picks the starting
// phase and every signature from the finished round is cleared. Round-scoped
// checkpoints that live outside the domain state (the applied delivery record
// and the integration result) are the caller's responsibility to clear.
func (s *State) Reopen() (Snapshot, error) {
	s.mu.Lock()
	if err := s.workflow().reopen(s); err != nil {
		snap := s.snapshotLocked()
		s.mu.Unlock()
		return snap, err
	}
	snap, hook := s.snapshotLocked(), s.onChange
	s.mu.Unlock()
	notify(hook, snap)
	return snap, nil
}

// EscalateToGoal transitions an active Fast mode session to Goal mode.
// It switches mode to ModeGoal and phase to PhasePlan.
// It seeds a shared plan v1 containing the escalation reason and any prior notes / verification issues.
// Approvals are reset so both agents must co-design and approve the plan before execution.
// If the session is already Goal mode, it returns ErrAlreadyGoal.
// If the session is PhaseDone, it returns ErrProjectDone.
func (s *State) EscalateToGoal(reason string) (Snapshot, error) {
	s.mu.Lock()
	if s.phase == PhaseDone {
		snap := s.snapshotLocked()
		s.mu.Unlock()
		return snap, ErrProjectDone
	}
	if s.mode == ModeGoal {
		snap := s.snapshotLocked()
		s.mu.Unlock()
		return snap, ErrAlreadyGoal
	}

	s.escalateToGoalLocked(reason)
	snap, hook := s.snapshotLocked(), s.onChange
	s.mu.Unlock()
	notify(hook, snap)
	return snap, nil
}

// SwitchMode changes the workflow at any point in a live session. A running
// round cannot be half-Fast and half-Goal: the plan, queue, phase and every
// signature belong to the mode that produced them, so switching starts a new
// round in the target mode. A switch from DONE changes the mode for the next
// task and leaves the current round finished.
//
// Fast → Goal delegates to the escalation rule so its seeded plan and prior
// notes stay intact. Every other transition uses the generic reset below.
func (s *State) SwitchMode(target Mode, reason string) (Snapshot, error) {
	if target != ModeFast && target != ModeGoal && target != ModeStep {
		return Snapshot{}, fmt.Errorf("unknown Duo mode %q (expected fast, goal or step)", target)
	}

	s.mu.Lock()
	if s.mode == target {
		snap := s.snapshotLocked()
		s.mu.Unlock()
		return snap, nil
	}
	if s.mode == ModeFast && target == ModeGoal && s.phase != PhaseDone {
		s.escalateToGoalLocked(reason)
	} else {
		s.switchModeLocked(target, reason)
	}
	snap, hook := s.snapshotLocked(), s.onChange
	s.mu.Unlock()
	notify(hook, snap)
	return snap, nil
}

// escalateToGoalLocked is the historical Fast → Goal transition. It is shared
// with SwitchMode so an escalation through either API seeds the same plan.
func (s *State) escalateToGoalLocked(reason string) {
	s.mode = ModeGoal
	s.phase = PhasePlan

	var b strings.Builder
	b.WriteString("# Escalated Goal Plan\n\n")
	if reason = strings.TrimSpace(reason); reason != "" {
		b.WriteString("## Escalation Reason\n")
		b.WriteString(reason)
		b.WriteString("\n\n")
	}
	if austinNote := strings.TrimSpace(s.notes[protocol.Austin]); austinNote != "" {
		b.WriteString("## Prior Austin Note\n")
		b.WriteString(austinNote)
		b.WriteString("\n\n")
	}
	if verNote := strings.TrimSpace(s.verification.Note); verNote != "" {
		b.WriteString("## Prior Verification Note\n")
		b.WriteString(verNote)
		b.WriteString("\n\n")
	}
	b.WriteString("## Next Steps\n- [ ] Austin and Tony align on architecture and finalize plan\n- [ ] Approve plan via duo_set_status\n- [ ] Execute implementation\n- [ ] Review and integrate\n")

	if strings.TrimSpace(s.plan) == "" {
		s.plan = b.String()
		s.planVersion = 1
	} else {
		if reason != "" {
			s.plan = fmt.Sprintf("## Escalation Reason: %s\n\n%s", reason, s.plan)
		}
		s.planVersion++
	}

	s.resetApprovalsLocked()
	s.verification = Verification{}
	s.started = true
	s.lastMutation = time.Now()
}

// switchModeLocked starts a new round in the target mode. Plan revision history
// is deliberately not carried across a Fast/Goal/Step switch: the old plan and
// queue described work under different gates, and keeping either would let a
// new mode inherit approvals it never earned.
func (s *State) switchModeLocked(target Mode, reason string) {
	reason = strings.TrimSpace(reason)
	s.mode = target
	s.resetApprovalsLocked()
	s.verification = Verification{}
	s.steps, s.step = nil, 0
	s.started = true
	s.lastMutation = time.Now()

	if s.phase == PhaseDone {
		s.plan = ""
		s.planVersion = 0
		return
	}

	switch target {
	case ModeFast:
		s.plan = ""
		s.planVersion = 0
		s.phase = PhaseRunning

	case ModeGoal:
		s.phase = PhasePlan
		s.plan = "# Goal Plan\n\nAustin and Tony agree on the design before either implements it.\n"
		if reason != "" {
			s.plan = "## Mode Switch Reason: " + reason + "\n\n" + s.plan
		}
		s.planVersion = 1

	case ModeStep:
		s.phase = PhasePlan
		s.plan = "# Step Plan\n\nAustin and Tony agree on the queue. Tag each item [fast] or [goal].\n"
		if reason != "" {
			s.plan = "## Mode Switch Reason: " + reason + "\n\n" + s.plan
		}
		s.planVersion = 1
	}
}

// RevokeReady invalidates one agent's claim. In Goal it clears a phase
// signature; in Fast the only claim to invalidate is Austin's completion
// request, so VERIFY falls back to RUNNING.
func (s *State) RevokeReady(agent protocol.AgentID, note string) Snapshot {
	s.mu.Lock()
	if agent == protocol.Austin || agent == protocol.Tony {
		if s.mode == ModeFast {
			if s.phase == PhaseVerify {
				s.phase = PhaseRunning
				s.verification = Verification{Status: VerificationNone, Note: strings.TrimSpace(note)}
			}
		} else {
			s.ready[agent] = false
			s.notes[agent] = strings.TrimSpace(note)
			s.evidence[agent] = ""
		}
		s.lastMutation = time.Now()
	}
	snap, hook := s.snapshotLocked(), s.onChange
	s.mu.Unlock()
	notify(hook, snap)
	return snap
}

// SetPersistence installs a callback invoked with the latest snapshot after
// every domain mutation. The callback is called outside the state lock, so it
// may perform I/O or take other locks. State stays a pure domain object and
// never touches disk itself.
func (s *State) SetPersistence(fn func(Snapshot)) {
	s.mu.Lock()
	s.onChange = fn
	s.mu.Unlock()
}

// Restore replaces the whole domain state with a persisted snapshot. It is used
// on resume, before persistence is enabled, so it does not cause a write-back.
// The persisted mode decides which phases are legal, so a Fast session can never
// be restored into a Goal phase and a legacy session (no mode) stays Goal.
func (s *State) Restore(snap Snapshot) error {
	mode := snap.EffectiveMode()
	if !mode.validPhase(snap.Phase) {
		return fmt.Errorf("persisted phase %q is not valid for %s mode", snap.Phase, mode)
	}
	if snap.PlanVersion < 0 {
		return fmt.Errorf("invalid persisted plan version %d", snap.PlanVersion)
	}

	s.mu.Lock()
	s.mode = mode
	s.phase = snap.Phase
	s.plan = snap.Plan
	s.planVersion = snap.PlanVersion
	s.ready[protocol.Austin] = snap.Ready[protocol.Austin]
	s.ready[protocol.Tony] = snap.Ready[protocol.Tony]
	s.notes[protocol.Austin] = snap.Notes[protocol.Austin]
	s.notes[protocol.Tony] = snap.Notes[protocol.Tony]
	s.evidence[protocol.Austin] = snap.Evidence[protocol.Austin]
	s.evidence[protocol.Tony] = snap.Evidence[protocol.Tony]
	s.verification = snap.Verification
	s.started = snap.Started
	// The queue is Step-mode state, so it is restored verbatim. The cursor is
	// re-derived from the queue: a snapshot written before the cursor existed, or
	// hand-edited, then agrees with the queue it describes.
	s.steps = append(Steps(nil), snap.Steps...)
	s.step = FirstOpenStep(s.steps)
	s.lastMutation = time.Now()
	out, hook := s.snapshotLocked(), s.onChange
	s.mu.Unlock()
	notify(hook, out)
	return nil
}

func notify(hook func(Snapshot), snap Snapshot) {
	if hook != nil {
		hook(snap)
	}
}

func (s *State) resetApprovalsLocked() {
	s.ready[protocol.Austin] = false
	s.ready[protocol.Tony] = false
	s.notes[protocol.Austin] = ""
	s.notes[protocol.Tony] = ""
	s.evidence[protocol.Austin] = ""
	s.evidence[protocol.Tony] = ""
}
