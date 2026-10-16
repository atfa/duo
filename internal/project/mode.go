package project

import (
	"fmt"
	"strings"
)

// Mode selects which Duo workflow a session runs. It is fixed for the lifetime
// of a session: Fast and Goal never mix, and nothing switches modes at runtime.
type Mode string

const (
	// ModeFast is the default workflow. Austin drives the task and Tony
	// independently verifies the result. It has no shared plan and no phase
	// sign-off: verification is the only gate before delivery.
	ModeFast Mode = "fast"
	// ModeGoal is the explicit heavy workflow: a negotiated shared plan,
	// independent execution, cross-review and dual final approval.
	ModeGoal Mode = "goal"
	// ModeStep runs the Goal cycle once per step of a queue held in the shared
	// plan, delivering each step to the user's repository before the next one
	// starts. It is Goal repeated, not a lighter Goal: the per-step gate is the
	// full EXECUTE → REVIEW → INTEGRATE sign-off.
	ModeStep Mode = "step"

	// DefaultMode is the workflow of a brand-new session.
	DefaultMode = ModeFast
)

// ParseMode resolves a persisted or operator-provided mode. A missing mode means
// Goal, never Fast: every session persisted before modes existed was a Goal
// session, and reinterpreting it as Fast would silently drop its plan and
// sign-off semantics. Fresh sessions get DefaultMode in the CLI instead.
func ParseMode(value string) (Mode, error) {
	switch Mode(strings.ToLower(strings.TrimSpace(value))) {
	case "":
		return ModeGoal, nil
	case ModeFast:
		return ModeFast, nil
	case ModeGoal:
		return ModeGoal, nil
	case ModeStep:
		return ModeStep, nil
	default:
		return "", fmt.Errorf("unknown Duo mode %q (expected fast, goal or step)", value)
	}
}

// Normalize is the single rule that turns a persisted or zero mode into the mode
// that runs. Only an explicitly recorded mode is honored: an absent mode, and any
// value this binary does not know, is Goal, because every session persisted before
// modes existed was a Goal session. Every reader goes through here so the rule
// cannot drift between state, snapshot, recovery and harness.
func (m Mode) Normalize() Mode {
	switch m {
	case ModeFast, ModeStep:
		return m
	default:
		return ModeGoal
	}
}

func (m Mode) String() string { return string(m) }

// Display returns the mode as shown in the UI.
func (m Mode) Display() string {
	switch m {
	case ModeFast:
		return "FAST"
	case ModeGoal:
		return "GOAL"
	default:
		return strings.ToUpper(string(m))
	}
}

// initialPhase is the phase a brand-new session starts in.
func (m Mode) initialPhase() Phase {
	if m == ModeFast {
		return PhaseRunning
	}
	// Step starts in PLAN because the step queue is the plan: a Step session with
	// no queue has nothing to run.
	return PhasePlan
}

// validPhase reports whether a phase belongs to this mode. Recovery and Restore
// use it so a persisted Goal phase can never be loaded into a Fast session (or
// the reverse) after a schema change or a hand-edited state.json.
func (m Mode) validPhase(p Phase) bool {
	switch m {
	case ModeFast:
		switch p {
		case PhaseRunning, PhaseVerify, PhaseDone:
			return true
		}
	default:
		// Step runs the Goal phase set; the difference between the two modes is the
		// chain, not the phases.
		switch p {
		case PhasePlan, PhaseExecute, PhaseReview, PhaseIntegrate, PhaseDone:
			return true
		}
	}
	return false
}

// Next returns the next phase of this mode's chain. Fast and Goal have
// different chains, so the chain belongs to the mode rather than to a shared
// global phase sequence that both modes would have to special-case.
func (m Mode) Next(p Phase) (Phase, bool) {
	if m == ModeFast {
		switch p {
		case PhaseRunning:
			return PhaseVerify, true
		case PhaseVerify:
			return PhaseDone, true
		default:
			return p, false
		}
	}
	// Step runs Goal's chain. The step loop-back (INTEGRATE → EXECUTE) is a phase
	// moving backwards, and Next is the linear chain recovery reads to prove a
	// session never rewinds, so the loop lives in stepWorkflow, not here.
	switch p {
	case PhasePlan:
		return PhaseExecute, true
	case PhaseExecute:
		return PhaseReview, true
	case PhaseReview:
		return PhaseIntegrate, true
	case PhaseIntegrate:
		return PhaseDone, true
	default:
		return p, false
	}
}
