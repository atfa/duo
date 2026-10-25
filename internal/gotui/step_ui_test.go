package gotui

import (
	"image"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/session"
	ui "github.com/metaspartan/gotui/v5"
)

func TestStepGateChainFastMapsPhases(t *testing.T) {
	step := project.Step{Title: "ship it"}
	chain := modeChain(project.ModeFast.String())
	for _, tc := range []struct {
		phase project.Phase
		stage string
	}{{project.PhaseExecute, "RUNNING"}, {project.PhaseReview, "VERIFY"}, {project.PhaseIntegrate, "DONE"}, {project.PhaseDone, "DONE"}} {
		gotChain, stage := stepGateChain(step, tc.phase)
		if stage != tc.stage {
			t.Fatalf("FAST step phase %s stage = %q, want %q", tc.phase, stage, tc.stage)
		}
		if len(gotChain) != len(chain) {
			t.Fatalf("FAST step chain = %v, want %v", gotChain, chain)
		}
	}
	// A landed step is done regardless of the phase the queue has moved to.
	_, stage := stepGateChain(project.Step{Title: "ship it", DeliveryHead: "abc123"}, project.PhaseReview)
	if stage != "DONE" {
		t.Fatalf("landed FAST step stage = %q, want DONE", stage)
	}
}

func TestStepGateChainGoalPassesPhasesThrough(t *testing.T) {
	step := project.Step{Title: "co-design", Policy: project.ModeGoal}
	chain := modeChain(project.ModeGoal.String())
	for _, phase := range []project.Phase{project.PhasePlan, project.PhaseExecute, project.PhaseReview, project.PhaseIntegrate, project.PhaseDone} {
		gotChain, stage := stepGateChain(step, phase)
		if stage != string(phase) {
			t.Fatalf("GOAL step phase %s stage = %q, want passthrough", phase, stage)
		}
		if len(gotChain) != len(chain) {
			t.Fatalf("GOAL step chain = %v, want %v", gotChain, chain)
		}
	}
}

func TestStageChainRunsMarksStages(t *testing.T) {
	tset := newTheme()
	current := ui.Style{Fg: tset.Bg, Bg: tset.Accent, Modifier: ui.ModifierBold}
	done := ui.Style{Fg: tset.OK, Bg: tset.Panel}
	runs := stageChainRuns([]string{"PLAN", "STEPS", "DONE"}, "STEPS", project.ModeStep, tset)
	if len(runs) != 3 {
		t.Fatalf("runs = %d, want 3", len(runs))
	}
	want := []struct {
		text  string
		style ui.Style
	}{{"✓ PLAN", done}, {"  ▶ STEPS", current}, {"  ○ DONE", tset.Subtle}}
	for i, w := range want {
		if runs[i].text != w.text || runs[i].style != w.style {
			t.Fatalf("run %d = {%q %v}, want {%q %v}", i, runs[i].text, runs[i].style, w.text, w.style)
		}
	}
}

func TestPackChainRunsKeepsTokensWhole(t *testing.T) {
	runs := stageChainRuns([]string{"RUNNING", "VERIFY", "DONE"}, "VERIFY", project.ModeFast, newTheme())
	// Barely wider than one token: each stage must move as one unit instead of
	// stranding a marker at a row's edge.
	lines := packChainRuns(runs, 10)
	if len(lines) != 3 {
		t.Fatalf("packed into %d lines, want 3", len(lines))
	}
	want := []string{"✓ RUNNING", "▶ VERIFY", "○ DONE"}
	for i, w := range want {
		if got := lines[i].text(); got != w {
			t.Fatalf("line %d = %q, want %q", i, got, w)
		}
	}
}

func TestWorkflowPanelDrawsStepGate(t *testing.T) {
	snap := project.Snapshot{
		Mode:  project.ModeStep,
		Phase: project.PhaseReview,
		Step:  1,
		Steps: []project.Step{{Title: "ship it"}},
	}
	w := newWorkflow(newTheme())
	w.view = session.View{Project: snap}
	w.SetRect(0, 0, 48, 10)
	buf := ui.NewBuffer(w.GetRect())
	w.Draw(buf)
	text := bufferText(buf)
	for _, want := range []string{"step gate", "[FAST]", "▶ VERIFY"} {
		if !strings.Contains(text, want) {
			t.Fatalf("panel text missing %q:\n%s", want, text)
		}
	}
	// The queue lifecycle row is still the top-level chain.
	if !strings.Contains(text, "STEPS") {
		t.Fatalf("panel text missing queue stage STEPS:\n%s", text)
	}
}

func bufferText(buf *ui.Buffer) string {
	var b strings.Builder
	for y := buf.Bounds().Min.Y; y < buf.Bounds().Max.Y; y++ {
		for x := buf.Bounds().Min.X; x < buf.Bounds().Max.X; x++ {
			r, _, _ := buf.GetCell(image.Pt(x, y)).Rune, 0, 0
			b.WriteRune(r)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

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
