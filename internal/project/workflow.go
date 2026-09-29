package project

import (
	"fmt"
	"strings"
	"time"

	"github.com/atfa/duo/internal/protocol"
)

// workflow is the mode-specific phase policy. State owns storage, locking and
// notification; the workflow owns exactly which transitions an event may cause.
// Both mode rules live behind this one interface, which is what keeps
// `fast`/`goal` checks out of the coordinator, TUI, persistence and prompts.
//
// Implementations mutate the State they are given and run with State.mu held by
// the caller; they must not lock, notify or touch the filesystem.
type workflow interface {
	// setPlan validates and applies a shared-plan change.
	setPlan(s *State, plan string) error
	// setReady applies one agent's ready/not-ready event.
	setReady(s *State, agent protocol.AgentID, ready bool, note, evidence string) (Transition, error)
	// setVerification applies a structured verification result.
	setVerification(s *State, agent protocol.AgentID, result VerificationResult, note, head string) (Transition, error)
	// complete performs the final agent-work → DONE transition after delivery.
	complete(s *State) error
	// reopen starts a new round from a finished DONE session so a follow-up
	// human task can be worked and verified again. The mode decides the
	// starting phase, and every signature from the finished round is cleared.
	reopen(s *State) error
}

func (s *State) workflow() workflow {
	if s.mode == ModeFast {
		return fastWorkflow{}
	}
	return goalWorkflow{}
}

// goalWorkflow is the original PLAN → EXECUTE → REVIEW → INTEGRATE → DONE
// policy, extracted verbatim so Goal behavior and its tests stay identical.
type goalWorkflow struct{}

func (goalWorkflow) setPlan(s *State, plan string) error {
	if s.phase != PhasePlan {
		return fmt.Errorf("%w: shared plan can only be changed during PLAN", ErrWrongPhase)
	}
	s.plan = plan
	s.planVersion++
	s.resetApprovalsLocked()
	s.started = true
	s.lastMutation = time.Now()
	return nil
}

func (goalWorkflow) setReady(s *State, agent protocol.AgentID, ready bool, note, evidence string) (Transition, error) {
	if s.phase == PhaseDone {
		return Transition{}, ErrProjectDone
	}
	if s.phase == PhasePlan && ready && strings.TrimSpace(s.plan) == "" {
		return Transition{}, ErrMissingPlan
	}

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

	var transition Transition
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
			if next, ok := ModeGoal.Next(s.phase); ok {
				s.phase = next
				s.resetApprovalsLocked()
				s.lastMutation = time.Now()
				transition.Next = next
			}
		}
	}
	return transition, nil
}

func (goalWorkflow) setVerification(*State, protocol.AgentID, VerificationResult, string, string) (Transition, error) {
	return Transition{}, fmt.Errorf("%w: structured verification is only used in Fast mode", ErrModeMismatch)
}

// complete performs the explicit INTEGRATE → DONE transition after the final
// artifact has been handed back to the user's repository. It requires a real
// dual sign-off on INTEGRATE and intentionally keeps both signatures and their
// evidence, so DONE records the final approval history instead of an empty one.
func (goalWorkflow) complete(s *State) error {
	if s.phase != PhaseIntegrate {
		return fmt.Errorf("%w: delivery can only complete from INTEGRATE", ErrWrongPhase)
	}
	if !s.ready[protocol.Austin] || !s.ready[protocol.Tony] {
		return fmt.Errorf("%w: final delivery requires both agents to have signed INTEGRATE", ErrWrongPhase)
	}
	if strings.TrimSpace(s.evidence[protocol.Austin]) == "" || strings.TrimSpace(s.evidence[protocol.Tony]) == "" {
		return fmt.Errorf("%w: final delivery requires signed evidence from both agents", ErrWrongPhase)
	}
	s.phase = PhaseDone
	s.lastMutation = time.Now()
	return nil
}

// reopen starts a new Goal round from PLAN. A new task invalidates the previous
// plan and both signatures, so the shared plan must be negotiated again before
// the phase chain can advance.
func (goalWorkflow) reopen(s *State) error {
	if s.phase != PhaseDone {
		return fmt.Errorf("%w: only a DONE session can be reopened", ErrWrongPhase)
	}
	s.phase = PhasePlan
	s.plan = ""
	s.resetApprovalsLocked()
	s.started = true
	s.lastMutation = time.Now()
	return nil
}

// fastWorkflow is the RUNNING → VERIFY → DONE policy. Austin alone drives work
// and requests verification; Tony alone verifies. There is no shared plan and
// no sign-off map: `setReady` is Austin's completion request, not a vote.
type fastWorkflow struct{}

func (fastWorkflow) setPlan(*State, string) error {
	return fmt.Errorf("%w: Fast mode has no shared plan; Austin drives the task directly (use `duo --mode goal` for the planned workflow)", ErrModeMismatch)
}

func (fastWorkflow) setReady(s *State, agent protocol.AgentID, ready bool, note, evidence string) (Transition, error) {
	if s.phase == PhaseDone {
		return Transition{}, ErrProjectDone
	}
	if agent != protocol.Austin {
		return Transition{}, fmt.Errorf("%w: Fast mode has no phase sign-off; Tony reports verification with duo_set_verification", ErrModeMismatch)
	}

	switch s.phase {
	case PhaseRunning:
		if !ready {
			// Withdrawing a request that was never made is a no-op.
			return Transition{}, nil
		}
		head := strings.TrimSpace(evidence)
		if head == "" {
			return Transition{}, fmt.Errorf("%w: verification request needs a committed Austin HEAD", ErrWrongPhase)
		}
		s.phase = PhaseVerify
		s.verification = Verification{Status: VerificationNone, Head: head, Note: strings.TrimSpace(note)}
		s.notes[protocol.Austin] = strings.TrimSpace(note)
		s.started = true
		s.lastMutation = time.Now()
		return Transition{}, nil

	case PhaseVerify:
		if ready {
			// Idempotent retry of an already requested verification.
			return Transition{}, nil
		}
		// Austin withdrew the completion request before Tony reported.
		s.phase = PhaseRunning
		s.verification = Verification{}
		s.lastMutation = time.Now()
		return Transition{}, nil

	default:
		return Transition{}, fmt.Errorf("%w: Fast mode does not run in phase %s", ErrWrongPhase, s.phase)
	}
}

func (fastWorkflow) setVerification(s *State, agent protocol.AgentID, result VerificationResult, note, head string) (Transition, error) {
	if s.phase == PhaseDone {
		return Transition{}, ErrProjectDone
	}
	if agent != protocol.Tony {
		return Transition{}, fmt.Errorf("%w: only Tony can report Fast-mode verification", ErrModeMismatch)
	}
	if s.phase != PhaseVerify {
		return Transition{}, fmt.Errorf("%w: verification can only be reported during VERIFY", ErrWrongPhase)
	}

	note = strings.TrimSpace(note)
	switch result {
	case VerificationPassed:
		if head != "" && !s.verification.Targets(head) {
			return Transition{}, fmt.Errorf("%w: verification is bound to Austin HEAD %s, not %s; verify the current artifact", ErrWrongPhase, shortHash(s.verification.Head), shortHash(head))
		}
		// `at` records when the verdict was formed, matching the Goal evidence
		// rule that a signature describes one exact artifact.
		at := time.Now()
		s.verification = Verification{Status: VerificationPassed, Head: s.verification.Head, Note: note, At: &at}
		s.lastMutation = time.Now()
		return Transition{ReadyForDelivery: true}, nil

	case VerificationIssueFound:
		if note == "" {
			return Transition{}, ErrMissingIssue
		}
		at := time.Now()
		s.verification = Verification{Status: VerificationIssueFound, Head: s.verification.Head, Note: note, At: &at}
		s.phase = PhaseRunning
		s.lastMutation = time.Now()
		return Transition{}, nil

	default:
		return Transition{}, fmt.Errorf("unknown verification result %q", result)
	}
}

func (fastWorkflow) complete(s *State) error {
	if s.phase != PhaseVerify {
		return fmt.Errorf("%w: Fast mode can only complete from VERIFY", ErrWrongPhase)
	}
	if s.verification.Status != VerificationPassed {
		return fmt.Errorf("%w: Fast mode requires a passed verification before DONE", ErrWrongPhase)
	}
	s.phase = PhaseDone
	s.lastMutation = time.Now()
	return nil
}

// reopen starts a new Fast round from RUNNING with no pending request and no
// stale verification, so Austin can drive a follow-up task and request a fresh
// independent verdict once it is complete.
func (fastWorkflow) reopen(s *State) error {
	if s.phase != PhaseDone {
		return fmt.Errorf("%w: only a DONE session can be reopened", ErrWrongPhase)
	}
	s.phase = PhaseRunning
	s.verification = Verification{}
	s.resetApprovalsLocked()
	s.started = true
	s.lastMutation = time.Now()
	return nil
}

func shortHash(hash string) string {
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return "(none)"
	}
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}
