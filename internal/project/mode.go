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
	default:
		return "", fmt.Errorf("unknown Duo mode %q (expected fast or goal)", value)
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
