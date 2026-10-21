package gotui

import (
	"testing"

	"github.com/atfa/duo/internal/project"
)

func TestStepWorkflowUsesQueueChain(t *testing.T) {
	chain := modeChain(project.ModeStep.String())
	want := []string{"PLAN", "STEPS", "DONE"}
	if len(chain) != len(want) {
		t.Fatalf("STEP chain = %v, want %v", chain, want)
	}
	for i := range want {
		if chain[i] != want[i] {
			t.Fatalf("STEP chain = %v, want %v", chain, want)
		}
	}

	for _, phase := range []project.Phase{project.PhaseExecute, project.PhaseReview, project.PhaseIntegrate} {
		snap := project.Snapshot{Mode: project.ModeStep, Phase: phase}
		if got := stepQueuePhase(snap); got != "STEPS" {
			t.Fatalf("phase %s maps to queue stage %q, want STEPS", phase, got)
		}
		if !phaseDone(project.ModeStep, "STEPS", "PLAN") {
			t.Fatalf("phase %s should show PLAN as done", phase)
		}
		if phaseDone(project.ModeStep, "STEPS", "DONE") {
			t.Fatalf("phase %s should not show DONE as reached", phase)
		}
	}
	if got := stepQueuePhase(project.Snapshot{Mode: project.ModeStep, Phase: project.PhasePlan}); got != "PLAN" {
		t.Fatalf("PLAN stage = %q", got)
	}
	if got := stepQueuePhase(project.Snapshot{Mode: project.ModeStep, Phase: project.PhaseDone}); got != "DONE" {
		t.Fatalf("DONE stage = %q", got)
	}
}
