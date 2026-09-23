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
	defer s.mu.Unlock()
	s.started = true
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
	defer s.mu.Unlock()

	if s.phase != PhasePlan {
		return s.snapshotLocked(), fmt.Errorf("%w: shared plan can only be changed during PLAN", ErrWrongPhase)
	}

	s.plan = plan
	s.planVersion++
	s.resetApprovalsLocked()
	s.started = true
	s.lastMutation = time.Now()

	return s.snapshotLocked(), nil
}

func (s *State) SetReady(agent protocol.AgentID, ready bool, note, evidence string) (Snapshot, Transition, error) {
	if agent != protocol.Austin && agent != protocol.Tony {
		return Snapshot{}, Transition{}, ErrUnknownAgent
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.phase == PhaseDone {
		return s.snapshotLocked(), Transition{}, ErrProjectDone
	}
	if s.phase == PhasePlan && ready && strings.TrimSpace(s.plan) == "" {
		return s.snapshotLocked(), Transition{}, ErrMissingPlan
	}

	previous := s.phase
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
		if next, ok := s.phase.Next(); ok {
			s.phase = next
			s.resetApprovalsLocked()
			s.lastMutation = time.Now()
			transition.Advanced = true
			transition.Next = next
		}
	}

	return s.snapshotLocked(), transition, nil
}

func (s *State) RevokeReady(agent protocol.AgentID, note string) Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	if agent == protocol.Austin || agent == protocol.Tony {
		s.ready[agent] = false
		s.notes[agent] = strings.TrimSpace(note)
		s.evidence[agent] = ""
		s.lastMutation = time.Now()
	}
	return s.snapshotLocked()
}

func (s *State) resetApprovalsLocked() {
	s.ready[protocol.Austin] = false
	s.ready[protocol.Tony] = false
	s.notes[protocol.Austin] = ""
	s.notes[protocol.Tony] = ""
	s.evidence[protocol.Austin] = ""
	s.evidence[protocol.Tony] = ""
}
