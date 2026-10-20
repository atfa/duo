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
	// setSteps sets or extends the Step-mode queue. Fast and Goal have no queue.
	setSteps(s *State, steps Steps, appendOnly bool) error
	// landStep records that the current step reached the user's repository.
	landStep(s *State, head string) (Transition, error)
	// revokeStep withdraws the current step's submission and review, returning the
	// session to EXECUTE with nothing pending for that step.
	revokeStep(s *State, reason string) (Transition, error)
	// reopen starts a new round from a finished DONE session so a follow-up
	// human task can be worked and verified again. The mode decides the
	// starting phase, and every signature from the finished round is cleared.
	reopen(s *State) error
}

func (s *State) workflow() workflow {
	switch s.mode {
	case ModeFast:
		return fastWorkflow{}
	case ModeStep:
		return stepWorkflow{}
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
			if next, ok := s.mode.Next(s.phase); ok {
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

// setSteps, landStep and revokeStep are Step-mode state. Goal has a plan, not a
// queue, so a caller that reaches them has picked the wrong mode.
func (goalWorkflow) setSteps(*State, Steps, bool) error {
	return fmt.Errorf("%w: Goal mode has a plan, not a step queue (use `duo --mode step`)", ErrModeMismatch)
}

func (goalWorkflow) landStep(*State, string) (Transition, error) {
	return Transition{}, fmt.Errorf("%w: only STEP mode lands steps", ErrModeMismatch)
}

func (goalWorkflow) revokeStep(*State, string) (Transition, error) {
	return Transition{}, fmt.Errorf("%w: only STEP mode has step submissions", ErrModeMismatch)
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

func (fastWorkflow) setSteps(*State, Steps, bool) error {
	return fmt.Errorf("%w: Fast mode has no step queue (use `duo --mode step`)", ErrModeMismatch)
}

func (fastWorkflow) landStep(*State, string) (Transition, error) {
	return Transition{}, fmt.Errorf("%w: only STEP mode lands steps", ErrModeMismatch)
}

func (fastWorkflow) revokeStep(*State, string) (Transition, error) {
	return Transition{}, fmt.Errorf("%w: only STEP mode has step submissions", ErrModeMismatch)
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

// stepWorkflow is Goal's phase set with a per-step gate. The queue is the
// dual-approved artifact in PLAN; each step then follows its own policy. A FAST
// step uses Tony's HEAD-bound verdict and lands from REVIEW. A GOAL step uses
// both agents' EXECUTE and REVIEW signatures, merges Tony's branch, and lands
// after both sign INTEGRATE. Either way a landed step returns the session to
// EXECUTE, and DONE on the last step still needs both agents.
type stepWorkflow struct{}

// setPlan takes the plan prose. The queue is separate state (setSteps), so a plan
// edit cannot move the cursor.
func (stepWorkflow) setPlan(s *State, plan string) error {
	if s.phase != PhasePlan {
		return fmt.Errorf("%w: the plan can only be changed during PLAN", ErrWrongPhase)
	}
	s.plan = plan
	s.planVersion++
	s.resetApprovalsLocked()
	s.started = true
	s.lastMutation = time.Now()
	return nil
}

// setSteps sets or extends the queue. In PLAN the queue is authored freely and
// needs both agents' approval, so a change resets the approvals. During EXECUTE
// only steps may be appended: a queue edit must never invalidate work that has
// already landed on the user's repository.
func (stepWorkflow) setSteps(s *State, steps Steps, appendOnly bool) error {
	if s.phase == PhaseDone {
		return ErrProjectDone
	}
	if len(steps) == 0 {
		return fmt.Errorf("%w: a Step session needs at least one step in its queue", ErrMissingPlan)
	}
	if s.phase == PhasePlan {
		s.steps = steps
		s.step = FirstOpenStep(s.steps)
		s.resetApprovalsLocked()
		s.started = true
		s.lastMutation = time.Now()
		return nil
	}
	if appendOnly {
		s.steps = append(s.steps, steps...)
		s.lastMutation = time.Now()
		return nil
	}
	return fmt.Errorf("%w: the queue can only be replaced during PLAN; during %s steps may be appended", ErrWrongPhase, s.phase)
}

func (stepWorkflow) setReady(s *State, agent protocol.AgentID, ready bool, note, evidence string) (Transition, error) {
	if s.phase == PhaseDone {
		return Transition{}, ErrProjectDone
	}

	switch s.phase {
	case PhasePlan:
		if ready && strings.TrimSpace(s.plan) == "" {
			return Transition{}, ErrMissingPlan
		}
		if ready && len(s.steps) == 0 {
			return Transition{}, fmt.Errorf("%w: a Step session needs a queue before it can leave PLAN", ErrMissingPlan)
		}
		s.ready[agent] = ready
		s.notes[agent] = strings.TrimSpace(note)
		if ready {
			s.evidence[agent] = strings.TrimSpace(evidence)
		} else {
			s.evidence[agent] = ""
		}
		s.started = true
		s.lastMutation = time.Now()
		if s.ready[protocol.Austin] && s.ready[protocol.Tony] {
			s.phase = PhaseExecute
			s.step = FirstOpenStep(s.steps)
			s.resetApprovalsLocked()
			s.lastMutation = time.Now()
			return Transition{Next: PhaseExecute}, nil
		}
		return Transition{}, nil

	case PhaseExecute:
		if s.currentStepPolicyLocked() == ModeGoal {
			// A Goal-policy step is joint work: each agent signs its own
			// committed HEAD and the step moves to cross-review only when both
			// have finished.
			s.ready[agent] = ready
			s.notes[agent] = strings.TrimSpace(note)
			if ready {
				s.evidence[agent] = strings.TrimSpace(evidence)
			} else {
				s.evidence[agent] = ""
			}
			s.started = true
			s.lastMutation = time.Now()
			if s.ready[protocol.Austin] && s.ready[protocol.Tony] {
				s.phase = PhaseReview
				s.resetApprovalsLocked()
				s.lastMutation = time.Now()
				return Transition{Next: PhaseReview}, nil
			}
			return Transition{}, nil
		}
		if agent != protocol.Austin {
			return Transition{}, fmt.Errorf("%w: in a FAST step Tony checks with duo_set_verification, not with a sign-off", ErrModeMismatch)
		}
		if !ready {
			return Transition{}, nil
		}
		head := strings.TrimSpace(evidence)
		if head == "" {
			return Transition{}, fmt.Errorf("%w: a step review request needs a committed Austin HEAD", ErrWrongPhase)
		}
		s.setStepHeadLocked(head)
		s.phase = PhaseReview
		s.notes[protocol.Austin] = strings.TrimSpace(note)
		s.started = true
		s.lastMutation = time.Now()
		return Transition{Next: PhaseReview}, nil

	case PhaseReview:
		if s.currentStepPolicyLocked() == ModeGoal {
			// Cross-review: each agent signs the peer's reviewed HEAD.
			s.ready[agent] = ready
			s.notes[agent] = strings.TrimSpace(note)
			if ready {
				s.evidence[agent] = strings.TrimSpace(evidence)
			} else {
				s.evidence[agent] = ""
			}
			s.started = true
			s.lastMutation = time.Now()
			if s.ready[protocol.Austin] && s.ready[protocol.Tony] {
				s.phase = PhaseIntegrate
				s.resetApprovalsLocked()
				s.lastMutation = time.Now()
				return Transition{Next: PhaseIntegrate}, nil
			}
			return Transition{}, nil
		}
		if agent != protocol.Austin {
			return Transition{}, nil
		}
		if ready {
			return Transition{}, nil
		}
		// Austin withdraws the request before Tony reports.
		s.phase = PhaseExecute
		s.clearStepVerdictLocked()
		s.lastMutation = time.Now()
		return Transition{Next: PhaseExecute}, nil

	case PhaseIntegrate:
		if s.currentStepPolicyLocked() == ModeGoal {
			// The GOAL-policy step signs the merged Austin HEAD. A non-final
			// step lands immediately after both signatures; the final step uses
			// the same signatures to finish the queue.
			s.ready[agent] = ready
			s.notes[agent] = strings.TrimSpace(note)
			if ready {
				s.evidence[agent] = strings.TrimSpace(evidence)
			} else {
				s.evidence[agent] = ""
			}
			s.lastMutation = time.Now()
			var t Transition
			if s.ready[protocol.Austin] && s.ready[protocol.Tony] {
				t.ReadyForDelivery = true
			}
			return t, nil
		}
		// The last FAST step ends the session, and that is the one place a step
		// needs both agents to agree it is finished. The test is the cursor, not
		// the ticks: the final step is signed and landed together, so at sign-off
		// time it is still unticked by definition. A non-final FAST step reaching
		// INTEGRATE means its delivery was blocked, and that is not something a
		// signature resolves.
		if s.step < len(s.steps) {
			return Transition{}, fmt.Errorf("%w: step %d of %d has not landed yet; sign-off is only for the final step", ErrWrongPhase, s.step, len(s.steps))
		}
		s.ready[agent] = ready
		s.notes[agent] = strings.TrimSpace(note)
		if ready {
			s.evidence[agent] = strings.TrimSpace(evidence)
		} else {
			s.evidence[agent] = ""
		}
		s.lastMutation = time.Now()
		var t Transition
		if s.ready[protocol.Austin] && s.ready[protocol.Tony] {
			t.ReadyForDelivery = true
		}
		return t, nil

	default:
		return Transition{}, fmt.Errorf("%w: STEP mode does not run in phase %s", ErrWrongPhase, s.phase)
	}
}

// setVerification is Tony's per-step check. The verdict binds to the exact Austin
// HEAD that was submitted, so a commit after the request revokes it — the same
// binding Fast mode enforces, and the reason the per-step gate needs no second
// signature.
func (stepWorkflow) setVerification(s *State, agent protocol.AgentID, result VerificationResult, note, head string) (Transition, error) {
	if s.phase == PhaseDone {
		return Transition{}, ErrProjectDone
	}
	if s.currentStepPolicyLocked() != ModeFast {
		return Transition{}, fmt.Errorf("%w: a GOAL-policy step is cross-reviewed and signed with duo_set_status", ErrModeMismatch)
	}
	if agent != protocol.Tony {
		return Transition{}, fmt.Errorf("%w: only Tony can report a FAST step verification", ErrModeMismatch)
	}
	if s.phase != PhaseReview {
		return Transition{}, fmt.Errorf("%w: a step can only be verified during REVIEW", ErrWrongPhase)
	}
	note = strings.TrimSpace(note)
	current := s.currentStepLocked()
	if current == nil {
		return Transition{}, fmt.Errorf("%w: the queue has no current step", ErrWrongPhase)
	}

	switch result {
	case VerificationPassed:
		if head != "" && !current.Verification.Targets(head) && current.Head != head {
			return Transition{}, fmt.Errorf("%w: the step was submitted at %s, not %s; review the current artifact", ErrWrongPhase, shortHash(current.Head), shortHash(head))
		}
		at := time.Now()
		current.Verification = Verification{Status: VerificationPassed, Head: current.Head, Note: note, At: &at}
		s.phase = PhaseIntegrate
		s.lastMutation = time.Now()
		return Transition{Next: PhaseIntegrate}, nil

	case VerificationIssueFound:
		if note == "" {
			return Transition{}, ErrMissingIssue
		}
		at := time.Now()
		current.Verification = Verification{Status: VerificationIssueFound, Head: current.Head, Note: note, At: &at}
		s.phase = PhaseExecute
		s.lastMutation = time.Now()
		return Transition{Next: PhaseExecute}, nil

	default:
		return Transition{}, fmt.Errorf("unknown verification result %q", result)
	}
}

// complete ends the session. It refuses while a step is unlanded: the queue, not a
// signature, decides when the work is over.
func (stepWorkflow) complete(s *State) error {
	if s.phase != PhaseIntegrate {
		return fmt.Errorf("%w: a STEP session can only complete from INTEGRATE", ErrWrongPhase)
	}
	if !QueueFinished(s.steps) {
		return fmt.Errorf("%w: step %d of %d has not landed", ErrWrongPhase, s.step, len(s.steps))
	}
	if !s.ready[protocol.Austin] || !s.ready[protocol.Tony] {
		return fmt.Errorf("%w: finishing a STEP session requires both agents to sign the finished queue", ErrWrongPhase)
	}
	s.phase = PhaseDone
	s.lastMutation = time.Now()
	return nil
}

// landStep records that the current step reached the user's repository and moves
// to the next one. It is the loop-back: INTEGRATE returns to EXECUTE while the
// queue has work, and stays in INTEGRATE on the last step so the finished queue
// can be signed off.
func (stepWorkflow) landStep(s *State, head string) (Transition, error) {
	if s.phase != PhaseIntegrate {
		return Transition{}, fmt.Errorf("%w: a step lands from INTEGRATE, not %s", ErrWrongPhase, s.phase)
	}
	current := s.currentStepLocked()
	if current == nil {
		return Transition{}, fmt.Errorf("%w: the queue has no current step to land", ErrWrongPhase)
	}
	// Landing is idempotent on the step record: a head that is already recorded as
	// landed on any step must not advance the queue a second time. The refusal names
	// the step that owns the artifact, not the cursor: "already landed as step 4" for
	// a head that landed as step 3 points the reader at the step being landed and
	// hides the record that made the landing redundant.
	for i, landed := range s.steps {
		if landed.DeliveryHead != "" && landed.DeliveryHead == head {
			return Transition{}, fmt.Errorf("%w: %s already landed as step %d", ErrWrongPhase, shortHash(head), i+1)
		}
	}
	if current.DeliveryHead != "" && current.DeliveryHead != head {
		return Transition{}, fmt.Errorf("%w: step %d already landed at %s", ErrWrongPhase, s.step, shortHash(current.DeliveryHead))
	}
	at := time.Now().UTC().Format(time.RFC3339)
	current.Done = true
	current.DeliveryHead = head
	current.DeliveredAt = at
	s.step = FirstOpenStep(s.steps)
	s.lastMutation = time.Now()
	if s.step > len(s.steps) {
		return Transition{}, nil
	}
	s.phase = PhaseExecute
	s.resetApprovalsLocked()
	return Transition{Next: PhaseExecute}, nil
}

// reopen starts a new queue from PLAN: a follow-up task is a new set of steps,
// and the new queue is dual-approved like the first one.
func (stepWorkflow) reopen(s *State) error {
	if s.phase != PhaseDone {
		return fmt.Errorf("%w: only a DONE session can be reopened", ErrWrongPhase)
	}
	s.phase = PhasePlan
	s.plan = ""
	s.steps, s.step = nil, 0
	s.resetApprovalsLocked()
	s.started = true
	s.lastMutation = time.Now()
	return nil
}

// revokeStep withdraws the current step's submission and its verdict. A review
// describes one exact HEAD; when that HEAD is no longer the artifact under the
// cursor, the review must not survive, or the step could land with a verdict about
// a different artifact. Clearing Head as well as Verification keeps the rendering
// honest: a step back in EXECUTE has nothing pending.
func (stepWorkflow) revokeStep(s *State, reason string) (Transition, error) {
	current := s.currentStepLocked()
	if current == nil {
		return Transition{}, fmt.Errorf("%w: the queue has no current step to revoke", ErrWrongPhase)
	}
	if s.phase != PhaseReview && s.phase != PhaseIntegrate {
		return Transition{}, fmt.Errorf("%w: a step can only be withdrawn from REVIEW or INTEGRATE, not %s", ErrWrongPhase, s.phase)
	}
	previous := s.phase
	current.Head = ""
	current.Verification = Verification{}
	if s.currentStepPolicyLocked() == ModeGoal {
		s.resetApprovalsLocked()
	}
	s.notes[protocol.Austin] = strings.TrimSpace(reason)
	s.phase = PhaseExecute
	s.lastMutation = time.Now()
	return Transition{Previous: previous, Next: PhaseExecute}, nil
}

// currentStepLocked returns the step the cursor points at, or nil.
func (s *State) currentStepLocked() *Step {
	if s.step < 1 || s.step > len(s.steps) {
		return nil
	}
	return &s.steps[s.step-1]
}

// currentStepPolicyLocked returns the policy of the step under the cursor. A
// finished or empty queue defaults to Fast; every caller checks the phase before
// using it, so this only keeps a zero queue from inventing a Goal sign-off.
func (s *State) currentStepPolicyLocked() Mode {
	if current := s.currentStepLocked(); current != nil {
		return current.EffectivePolicy()
	}
	return ModeFast
}

func (s *State) setStepHeadLocked(head string) {
	if current := s.currentStepLocked(); current != nil {
		current.Head = head
		current.Verification = Verification{}
	}
}

func (s *State) clearStepVerdictLocked() {
	if current := s.currentStepLocked(); current != nil {
		current.Verification = Verification{}
	}
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
