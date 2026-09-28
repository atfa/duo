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
}

// EffectiveMode normalizes a possibly-zero mode. Only an explicit "fast" is
// Fast; a legacy snapshot with no mode is Goal.
func (s Snapshot) EffectiveMode() Mode {
	if s.Mode == ModeFast {
		return ModeFast
	}
	return ModeGoal
}

func (s Snapshot) String() string {
	if s.EffectiveMode() == ModeFast {
		return s.fastString()
	}
	return s.goalString()
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
// not an explicit Fast mode is treated as Goal, so a zero value behaves like a
// legacy session.
func NewStateFor(mode Mode) *State {
	if mode != ModeFast {
		mode = ModeGoal
	}
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
