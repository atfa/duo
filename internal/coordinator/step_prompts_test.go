package coordinator

import (
	"strings"
	"testing"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
)

// stepSnapshot is a STEP session with a three-step queue, two landed and the
// second under work. Every STEP prompt has to name the current step, so the
// fixture is built to make a prompt that names the wrong one fail loudly.
func stepSnapshot(phase project.Phase) project.Snapshot {
	return project.Snapshot{
		Mode:        project.ModeStep,
		Phase:       phase,
		Plan:        "split the work",
		PlanVersion: 3,
		Step:        2,
		Steps: project.Steps{
			{Title: "add ModeStep", Done: true, DeliveryHead: "1111111111111111"},
			{Title: "land each step", Done: false, Head: "2222222222222222"},
			{Title: "restart on the new binary", Done: false},
		},
		// Evidence is a decoy on purpose: STEP keeps no per-step signature, so the
		// submitted HEAD must come from the step record. A REVIEW prompt that named
		// this value would be pointing at a HEAD nobody submitted.
		Evidence: map[protocol.AgentID]string{protocol.Austin: "9999999999999999"},
		Ready:    map[protocol.AgentID]bool{},
	}
}

func stepCoordinator() *Coordinator {
	coord := promptCoordinator()
	coord.SetDelivery(sessionstore.Delivery{TargetBranch: "main"})
	return coord
}

// TestStepExecutePromptNamesTheCurrentStep is the core of the mode: an agent that
// has to guess which step is current works the wrong one, so the prompt must carry
// the cursor, the title and the queue.
func TestStepExecutePromptNamesTheCurrentStep(t *testing.T) {
	coord := stepCoordinator()
	snap := stepSnapshot(project.PhaseExecute)

	austin := coord.phasePrompt(protocol.Austin, project.PhasePlan, project.PhaseExecute, snap, "")
	for _, want := range []string{"step 2/3", "land each step", "duo_set_status ready=true", "Do not start the next step", "do not tick a box"} {
		if !strings.Contains(austin, want) {
			t.Fatalf("Austin STEP EXECUTE prompt missing %q:\n%s", want, austin)
		}
	}
	// The queue is rendered from the Step array, so the prompt shows landed state.
	for _, want := range []string{"add ModeStep", "landed 111111111111", "restart on the new binary"} {
		if !strings.Contains(austin, want) {
			t.Fatalf("Austin STEP EXECUTE prompt must render the queue (%q missing):\n%s", want, austin)
		}
	}
	if strings.Contains(austin, "cross-review") {
		t.Fatalf("STEP EXECUTE must not leak the Goal cross-review duty:\n%s", austin)
	}

	tony := coord.phasePrompt(protocol.Tony, project.PhasePlan, project.PhaseExecute, snap, "")
	for _, want := range []string{"read-only on the delivered artifact", "fast-forwarded to the last landed step"} {
		if !strings.Contains(tony, want) {
			t.Fatalf("Tony STEP EXECUTE prompt missing %q:\n%s", want, tony)
		}
	}
}

// TestStepReviewPromptBindsTonyToTheExactHead pins the per-step gate: Tony's verb
// is duo_set_verification, it is bound to one HEAD, and passing lands the step.
func TestStepReviewPromptBindsTonyToTheExactHead(t *testing.T) {
	coord := stepCoordinator()
	snap := stepSnapshot(project.PhaseReview)

	tony := coord.phasePrompt(protocol.Tony, project.PhaseExecute, project.PhaseReview, snap, "")
	for _, want := range []string{"step 2/3", "land each step", "222222222222", "duo_set_verification", "land this step on main", "issue_found"} {
		if !strings.Contains(tony, want) {
			t.Fatalf("Tony STEP REVIEW prompt missing %q:\n%s", want, tony)
		}
	}
	// The reviewed HEAD comes from the step record, never from Evidence (which STEP
	// does not set) and never from the worktree (which would name a later, unreviewed
	// HEAD). Both traps are present in the fixture.
	if strings.Contains(tony, "999999999999") {
		t.Fatalf("Tony STEP REVIEW prompt used Evidence instead of the submitted step head:\n%s", tony)
	}
	if strings.Contains(tony, "/tmp/austin") {
		t.Fatalf("Tony STEP REVIEW prompt must not derive the head from the worktree:\n%s", tony)
	}
	if strings.Contains(tony, "Sign ready=true") {
		t.Fatalf("Tony must not sign a STEP step with duo_set_status:\n%s", tony)
	}

	austin := coord.phasePrompt(protocol.Austin, project.PhaseExecute, project.PhaseReview, snap, "")
	for _, want := range []string{"under Tony's verification", "Do not commit while it is under review"} {
		if !strings.Contains(austin, want) {
			t.Fatalf("Austin STEP REVIEW prompt missing %q:\n%s", want, austin)
		}
	}
}

// TestStepIntegratePromptSeparatesTheFinalStep pins the gate difference: a step
// landing needs no signature, the last step needs both.
func TestStepIntegratePromptSeparatesTheFinalStep(t *testing.T) {
	coord := stepCoordinator()

	middle := coord.phasePrompt(protocol.Austin, project.PhaseReview, project.PhaseIntegrate, stepSnapshot(project.PhaseIntegrate), "")
	for _, want := range []string{"landing it on main", "advancing the queue to step 3", "No agent action is needed"} {
		if !strings.Contains(middle, want) {
			t.Fatalf("non-final STEP INTEGRATE prompt missing %q:\n%s", want, middle)
		}
	}
	if strings.Contains(middle, "both agents must sign") {
		t.Fatalf("a mid-queue step must not ask for dual sign-off:\n%s", middle)
	}

	final := stepSnapshot(project.PhaseIntegrate)
	final.Step = 3
	finalPrompt := coord.phasePrompt(protocol.Tony, project.PhaseReview, project.PhaseIntegrate, final, "")
	for _, want := range []string{"FINAL step", "3/3", "both agents must sign INTEGRATE", "duo_set_status ready=true"} {
		if !strings.Contains(finalPrompt, want) {
			t.Fatalf("final STEP INTEGRATE prompt missing %q:\n%s", want, finalPrompt)
		}
	}
}

// TestStepResumePromptReinjectsTheQueue is what makes the restart path work: a
// STEP session can be resumed by a different Duo process on a new binary, so the
// queue has to arrive with the wake or an agent re-lands a step that already
// reached the user's repository.
func TestStepResumePromptReinjectsTheQueue(t *testing.T) {
	coord := promptCoordinator()
	if err := coord.project.Restore(stepSnapshot(project.PhaseExecute)); err != nil {
		t.Fatal(err)
	}

	got := coord.ResumePrompt(protocol.Austin)
	for _, want := range []string{"[Duo session resumed]", "Step: 2 of 3 — land each step", "add ModeStep", "landed 111111111111", "restart on the new binary", "do not re-land a step that is already ticked"} {
		if !strings.Contains(got, want) {
			t.Fatalf("STEP resume prompt missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Continue your assigned EXECUTE work") {
		t.Fatalf("STEP resume must not reuse the Goal EXECUTE instruction:\n%s", got)
	}
}

// TestStepResumePromptZhCarriesTheQueue keeps the Chinese path equivalent to the
// English one, including the queue and the no-re-land rule.
func TestStepResumePromptZhCarriesTheQueue(t *testing.T) {
	coord := promptCoordinator()
	coord.SetLanguage("zh")
	if err := coord.project.Restore(stepSnapshot(project.PhaseReview)); err != nil {
		t.Fatal(err)
	}

	got := coord.ResumePrompt(protocol.Tony)
	for _, want := range []string{"[Duo 会话已恢复]", "第 2/3 步", "land each step", "duo_set_verification", "不要重复落地已打勾的步骤"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Chinese STEP resume prompt missing %q: %s", want, got)
		}
	}
}

// TestStepPhasePromptsZhNameTheStep is the Chinese counterpart of the cursor rule.
func TestStepPhasePromptsZhNameTheStep(t *testing.T) {
	coord := stepCoordinator()
	coord.SetLanguage("zh")
	snap := stepSnapshot(project.PhaseExecute)

	austin := coord.phasePrompt(protocol.Austin, project.PhasePlan, project.PhaseExecute, snap, "")
	for _, want := range []string{"正在执行步骤 2/3", "land each step", "duo_set_status ready=true", "不要提前开始下一步", "不要自己打勾"} {
		if !strings.Contains(austin, want) {
			t.Fatalf("Chinese Austin STEP EXECUTE prompt missing %q: %s", want, austin)
		}
	}
	tony := coord.phasePrompt(protocol.Tony, project.PhaseExecute, project.PhaseReview, snap, "")
	for _, want := range []string{"duo_set_verification", "把该步骤落地到 main 并推进队列", "issue_found"} {
		if !strings.Contains(tony, want) {
			t.Fatalf("Chinese Tony STEP REVIEW prompt missing %q: %s", want, tony)
		}
	}
}

// TestStepPromptsTolerateAnEmptyQueue keeps a session that has not published a
// queue yet from rendering a nonsense cursor.
func TestStepPromptsTolerateAnEmptyQueue(t *testing.T) {
	coord := stepCoordinator()
	snap := project.Snapshot{Mode: project.ModeStep, Phase: project.PhasePlan}

	for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		got := coord.phasePrompt(agent, project.PhaseIntegrate, project.PhasePlan, snap, "")
		for _, want := range []string{"duo_set_steps", "no queue set"} {
			if !strings.Contains(got, want) {
				t.Fatalf("%s STEP PLAN prompt missing %q:\n%s", agent, want, got)
			}
		}
	}
}
