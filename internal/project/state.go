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
	ErrProjectDone  = errors.New("project is already DONE")
	ErrUnknownAgent = errors.New("unknown agent")
)

type Snapshot struct {
	Phase        Phase
	Plan         string
	PlanVersion  int
	Ready        map[protocol.AgentID]bool
	Notes        map[protocol.AgentID]string
	Evidence     map[protocol.AgentID]string
	Started      bool
	LastMutation time.Time
}

func (s Snapshot) String() string {
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
	// ReadyForDelivery is set when both agents sign INTEGRATE. Unlike earlier
	// phases this does not advance the phase: agent work is complete, but the
	// final artifact still has to be handed back to the user's repository before
	// the project can be considered DONE.
	ReadyForDelivery bool
}

type State struct {
	mu sync.RWMutex

	phase        Phase
	plan         string
	planVersion  int
	ready        map[protocol.AgentID]bool
	notes        map[protocol.AgentID]string
	evidence     map[protocol.AgentID]string
	started      bool
	lastMutation time.Time

	// onChange is invoked with the latest snapshot after every mutation. It is
	// called outside the lock; the state itself never touches disk.
	onChange func(Snapshot)
}

func NewState() *State {
	return &State{
		phase: PhasePlan,
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

func (s *State) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshotLocked()
}

func (s *State) snapshotLocked() Snapshot {
	return Snapshot{
		Phase:       s.phase,
		Plan:        s.plan,
		PlanVersion: s.planVersion,
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

	if s.phase != PhasePlan {
		snap := s.snapshotLocked()
		s.mu.Unlock()
		return snap, fmt.Errorf("%w: shared plan can only be changed during PLAN", ErrWrongPhase)
	}

	s.plan = plan
	s.planVersion++
	s.resetApprovalsLocked()
	s.started = true
	s.lastMutation = time.Now()

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

	if s.phase == PhaseDone {
		snap := s.snapshotLocked()
		s.mu.Unlock()
		return snap, Transition{}, ErrProjectDone
	}
	if s.phase == PhasePlan && ready && strings.TrimSpace(s.plan) == "" {
		snap := s.snapshotLocked()
		s.mu.Unlock()
		return snap, Transition{}, ErrMissingPlan
	}

	previous := s.phase
	wasReady := s.ready[agent]
	s.ready[agent] = ready
	s.notes[agent] = strings.TrimSpace(note)
	if ready {
		s.evidence[agent] = strings.TrimSpace(evidence)
	} else {
		s.evidence[agent] = ""
	}
	s.started = true
	s.lastMutation = time.Now()

	transition := Transition{Previous: previous, Next: previous}
	if s.ready[protocol.Austin] && s.ready[protocol.Tony] {
		switch s.phase {
		case PhaseIntegrate:
			// INTEGRATE is the final agent phase. Dual sign-off records final
			// approval and deliberately keeps both signatures: they describe the
			// artifact that still has to be delivered, and they stay visible in
			// DONE as the final approval history.
			// Final approval is an edge, not a level: retries of an already
			// accepted status must not start a second delivery transaction.
			transition.ReadyForDelivery = !wasReady && ready
		default:
			if next, ok := s.phase.Next(); ok {
				s.phase = next
				s.resetApprovalsLocked()
				s.lastMutation = time.Now()
				transition.Advanced = true
				transition.Next = next
			}
		}
	}

	snap, hook := s.snapshotLocked(), s.onChange
	s.mu.Unlock()
	notify(hook, snap)
	return snap, transition, nil
}

// Complete performs the explicit INTEGRATE → DONE transition after the final
// artifact has been handed back to the user's repository. It requires a real
// dual sign-off on INTEGRATE and intentionally keeps both signatures and their
// evidence, so DONE records the final approval history instead of an empty one.
func (s *State) Complete() (Snapshot, error) {
	s.mu.Lock()

	if s.phase != PhaseIntegrate {
		snap := s.snapshotLocked()
		s.mu.Unlock()
		return snap, fmt.Errorf("%w: delivery can only complete from INTEGRATE", ErrWrongPhase)
	}
	if !s.ready[protocol.Austin] || !s.ready[protocol.Tony] {
		snap := s.snapshotLocked()
		s.mu.Unlock()
		return snap, fmt.Errorf("%w: final delivery requires both agents to have signed INTEGRATE", ErrWrongPhase)
	}
	if strings.TrimSpace(s.evidence[protocol.Austin]) == "" || strings.TrimSpace(s.evidence[protocol.Tony]) == "" {
		snap := s.snapshotLocked()
		s.mu.Unlock()
		return snap, fmt.Errorf("%w: final delivery requires signed evidence from both agents", ErrWrongPhase)
	}

	s.phase = PhaseDone
	s.lastMutation = time.Now()

	snap, hook := s.snapshotLocked(), s.onChange
	s.mu.Unlock()
	notify(hook, snap)
	return snap, nil
}

func (s *State) RevokeReady(agent protocol.AgentID, note string) Snapshot {
	s.mu.Lock()
	if agent == protocol.Austin || agent == protocol.Tony {
		s.ready[agent] = false
		s.notes[agent] = strings.TrimSpace(note)
		s.evidence[agent] = ""
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
func (s *State) Restore(snap Snapshot) error {
	switch snap.Phase {
	case PhasePlan, PhaseExecute, PhaseReview, PhaseIntegrate, PhaseDone:
	default:
		return fmt.Errorf("unknown persisted phase %q", snap.Phase)
	}
	if snap.PlanVersion < 0 {
		return fmt.Errorf("invalid persisted plan version %d", snap.PlanVersion)
	}

	s.mu.Lock()
	s.phase = snap.Phase
	s.plan = snap.Plan
	s.planVersion = snap.PlanVersion
	s.ready[protocol.Austin] = snap.Ready[protocol.Austin]
	s.ready[protocol.Tony] = snap.Ready[protocol.Tony]
	s.notes[protocol.Austin] = snap.Notes[protocol.Austin]
	s.notes[protocol.Tony] = snap.Notes[protocol.Tony]
	s.evidence[protocol.Austin] = snap.Evidence[protocol.Austin]
	s.evidence[protocol.Tony] = snap.Evidence[protocol.Tony]
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
