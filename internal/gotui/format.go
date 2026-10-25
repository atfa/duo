package gotui

import (
	"fmt"
	"strings"
	"time"

	"github.com/atfa/duo/internal/project"
)

func humanTokens(n int) string {
	switch {
	case n <= 0:
		return "0"
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func shortDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	if d < time.Minute {
		return fmt.Sprintf("%.0fs", d.Seconds())
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

func spinnerFrame(frame int) string {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	return frames[frame%len(frames)]
}

func shortPath(path string, max int) string {
	if max < 8 || len(path) <= max {
		return path
	}
	parts := strings.Split(path, "/")
	if len(parts) <= 2 {
		return fitText(path, max)
	}
	out := "…/" + parts[len(parts)-1]
	for i := len(parts) - 2; i >= 0; i-- {
		candidate := "…/" + strings.Join(parts[i:], "/")
		if len(candidate) > max {
			break
		}
		out = candidate
	}
	return fitText(out, max)
}

func modeChain(mode string) []string {
	switch strings.ToUpper(mode) {
	case "FAST":
		return []string{"RUNNING", "VERIFY", "DONE"}
	case "STEP":
		// Step's queue uses the Goal phase set internally, but its top-level
		// workflow is only plan the queue, run it, finish. The current step's
		// Fast/Goal gate is shown separately, so Goal's REVIEW/INTEGRATE phases
		// do not masquerade as session-level stages here.
		return []string{"PLAN", "STEPS", "DONE"}
	default:
		return []string{"PLAN", "EXECUTE", "REVIEW", "INTEGRATE", "DONE"}
	}
}

// stepGateChain is the workflow of the queue step that is currently in flight,
// which STEP shows underneath its own queue lifecycle. The queue only ever runs
// one step at a time, so the session phase is that step's gate: a FAST step
// runs RUNNING → VERIFY → DONE and a GOAL step runs Goal's five phases.
func stepGateChain(step project.Step, phase project.Phase) (chain []string, stage string) {
	policy := step.EffectivePolicy()
	chain = modeChain(policy.String())
	if policy == project.ModeGoal {
		return chain, string(phase)
	}
	switch {
	// A landed step is complete even if a later phase is already running, and a
	// passed verdict has left REVIEW for INTEGRATE, where only delivery remains.
	case step.DeliveryHead != "" || phase == project.PhaseIntegrate || phase == project.PhaseDone:
		return chain, "DONE"
	case phase == project.PhaseReview:
		return chain, "VERIFY"
	default:
		return chain, "RUNNING"
	}
}
