package gotui

import (
	"strings"
	"testing"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/gdamore/tcell/v3"
	ui "github.com/metaspartan/gotui/v5"
)

func planLines(p *planOverlay) string {
	var b strings.Builder
	for _, line := range p.lines {
		b.WriteString(line.text())
		b.WriteByte('\n')
	}
	return b.String()
}

func placePlan(t *testing.T, app *App) {
	t.Helper()
	app.layout = app.computeLayout(120, 42)
	app.plan.place(app.layout, app.live, true)
}

func TestPlanOverlayShowsPlanAndLocalizedQueue(t *testing.T) {
	fake := newFakeSession()
	fake.view.Project = project.Snapshot{
		Mode:        project.ModeStep,
		Phase:       project.PhaseExecute,
		Plan:        "# Design\n\nUse a cache.\n\n- one\n- two",
		PlanVersion: 3,
		Steps: project.Steps{
			{Title: "first", Done: true, DeliveryHead: "abcdef123456"},
			{Title: "second", Policy: project.ModeGoal},
			{Title: "third"},
		},
		Started: true,
	}
	app := New(fake)
	app.openPlan()
	placePlan(t, app)

	text := planLines(app.plan)
	for _, want := range []string{
		"STEP", "EXECUTE", "v3", "PLAN", "Design", "Use a cache.",
		"✓ 1. [FAST] first", "landed", "abcdef12",
		"● 2. [GOAL] second", "current",
		"○ 3. [FAST] third", "todo",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("plan overlay missing %q:\n%s", want, text)
		}
	}
	// The agent-facing English prose from Steps.Render must not leak in.
	if strings.Contains(text, "submitted abc") {
		t.Fatalf("plan overlay rendered the agent queue prose:\n%s", text)
	}
}

func TestPlanOverlayOpensFromF3AndSlashAndScrolls(t *testing.T) {
	fake := newFakeSession()
	steps := make(project.Steps, 0, 30)
	for i := 1; i <= 30; i++ {
		steps = append(steps, project.Step{Title: "step " + string(rune('A'+i%26))})
	}
	fake.view.Project = project.Snapshot{
		Mode:    project.ModeStep,
		Phase:   project.PhasePlan,
		Plan:    "# Step Plan",
		Steps:   steps,
		Started: true,
	}
	app := New(fake)

	app.handleKey(tcell.NewEventKey(tcell.KeyF3, "", tcell.ModNone))
	if app.overlay != overlayPlan {
		t.Fatalf("F3 overlay = %v, want plan", app.overlay)
	}
	app.layout = app.computeLayout(120, 24)
	app.plan.place(app.layout, app.live, true)
	if app.plan.maxOffset() == 0 {
		t.Fatal("fixture is too short to scroll")
	}
	app.handleKey(tcell.NewEventKey(tcell.KeyPgDn, "", tcell.ModNone))
	if app.plan.offset == 0 {
		t.Fatal("PgDn did not scroll the plan overlay")
	}
	app.handleKey(tcell.NewEventKey(tcell.KeyEsc, "", tcell.ModNone))
	if app.overlay != overlayNone {
		t.Fatalf("Esc left overlay = %v, want none", app.overlay)
	}

	app.runSlash("/plan")
	if app.overlay != overlayPlan {
		t.Fatalf("/plan overlay = %v, want plan", app.overlay)
	}
}

func TestPlanOverlayEmptyStates(t *testing.T) {
	fast := newFakeSession()
	fast.view.Project = project.Snapshot{
		Mode:         project.ModeFast,
		Phase:        project.PhaseVerify,
		Started:      true,
		Verification: project.Verification{Status: project.VerificationPassed},
	}
	fast.view.Delivery = sessionstore.Delivery{Status: sessionstore.DeliveryPending}
	fastApp := New(fast)
	fastApp.openPlan()
	placePlan(t, fastApp)
	text := planLines(fastApp.plan)
	for _, want := range []string{"FAST has no shared plan.", "passed", "pending", "Ctrl+O opens session details."} {
		if !strings.Contains(text, want) {
			t.Fatalf("FAST empty state missing %q:\n%s", want, text)
		}
	}

	done := newFakeSession()
	done.view.Project = project.Snapshot{Mode: project.ModeGoal, Phase: project.PhaseDone, Started: true}
	doneApp := New(done)
	doneApp.openPlan()
	placePlan(t, doneApp)
	text = planLines(doneApp.plan)
	for _, want := range []string{"This round has no shared plan yet.", "This round is DONE; its plan was cleared.", "Ctrl+O opens session details."} {
		if !strings.Contains(text, want) {
			t.Fatalf("DONE empty state missing %q:\n%s", want, text)
		}
	}

	step := newFakeSession()
	step.view.Project = project.Snapshot{Mode: project.ModeStep, Phase: project.PhasePlan, Started: true}
	stepApp := New(step)
	stepApp.openPlan()
	placePlan(t, stepApp)
	if text := planLines(stepApp.plan); !strings.Contains(text, "No step queue yet.") {
		t.Fatalf("empty Step queue missing its notice:\n%s", text)
	}
}

func TestPlanOverlayIsTranslated(t *testing.T) {
	fake := newFakeSession()
	fake.view.Language = "zh"
	fake.view.Project = project.Snapshot{
		Mode:        project.ModeStep,
		Phase:       project.PhaseExecute,
		Plan:        "# 设计\n\n使用缓存。",
		PlanVersion: 2,
		Steps: project.Steps{
			{Title: "第一步", Done: true, DeliveryHead: "abcdef123456"},
			{Title: "第二步"},
		},
		Started: true,
	}
	app := New(fake)
	app.openPlan()
	placePlan(t, app)
	text := planLines(app.plan)
	for _, want := range []string{"阶段", "计划 v2", "设计", "已落地", "当前"} {
		if !strings.Contains(text, want) {
			t.Fatalf("zh plan overlay missing %q:\n%s", want, text)
		}
	}

	app.plan.Draw(ui.NewBuffer(app.plan.GetRect()))
	if got := strings.TrimSpace(app.plan.Title); got != "计划与步骤" {
		t.Fatalf("zh plan title = %q, want 计划与步骤", got)
	}
}

// A queue revision, a submission or a landing changes fields the panel renders
// without changing the step count, phase or plan version, so the cache key has
// to cover them: otherwise an open panel keeps showing the old markers.
func TestPlanOverlayRefreshesWhenAStepChanges(t *testing.T) {
	fake := newFakeSession()
	fake.view.Project = project.Snapshot{
		Mode:        project.ModeStep,
		Phase:       project.PhasePlan,
		Plan:        "# Step Plan",
		PlanVersion: 1,
		Steps:       project.Steps{{Title: "alpha"}, {Title: "beta"}},
		Started:     true,
	}
	app := New(fake)
	app.openPlan()
	placePlan(t, app)
	if strings.Contains(planLines(app.plan), "landed") {
		t.Fatalf("fresh queue already shows a landing:\n%s", planLines(app.plan))
	}

	view := fake.view
	steps := append(project.Steps{}, view.Project.Steps...)
	steps[0].Done = true
	steps[0].DeliveryHead = "abcdef123456789"
	view.Project.Steps = steps
	app.plan.place(app.layout, view, true)

	text := planLines(app.plan)
	for _, want := range []string{"✓ 1. [FAST] alpha", "landed", "abcdef12"} {
		if !strings.Contains(text, want) {
			t.Fatalf("plan overlay did not refresh after a landing, missing %q:\n%s", want, text)
		}
	}
}

// A shorter plan must clamp the viewport: otherwise Draw starts past the last
// line and the panel goes blank while the human is reading it.
func TestPlanOverlayClampsOffsetWhenThePlanShrinks(t *testing.T) {
	fake := newFakeSession()
	fake.view.Project = project.Snapshot{
		Mode:        project.ModeGoal,
		Phase:       project.PhasePlan,
		Plan:        "# Plan\n\n" + strings.Repeat("- detail line\n", 40),
		PlanVersion: 2,
		Started:     true,
	}
	app := New(fake)
	app.openPlan()
	app.layout = app.computeLayout(120, 20)
	app.plan.place(app.layout, app.live, true)
	app.plan.Scroll(app.plan.maxOffset())
	if app.plan.offset == 0 {
		t.Fatal("fixture is too short to scroll")
	}

	view := fake.view
	view.Project.Plan = "# Plan\n\none short line"
	view.Project.PlanVersion = 3
	app.plan.place(app.layout, view, true)

	if max := app.plan.maxOffset(); app.plan.offset > max {
		t.Fatalf("offset = %d after the plan shrank, want <= %d", app.plan.offset, max)
	}
	if got := planLines(app.plan); strings.TrimSpace(got) == "" {
		t.Fatal("panel is blank after the plan shrank")
	}
}

// A FAST round and a cleared DONE round have no plan, so the header must not
// advertise a version next to "no shared plan".
func TestPlanOverlayOmitsPlanVersionWithoutAPlan(t *testing.T) {
	for _, snap := range []project.Snapshot{
		{Mode: project.ModeFast, Phase: project.PhaseRunning, Started: true},
		{Mode: project.ModeGoal, Phase: project.PhaseDone, PlanVersion: 4, Started: true},
	} {
		fake := newFakeSession()
		fake.view.Project = snap
		app := New(fake)
		app.openPlan()
		placePlan(t, app)
		if text := planLines(app.plan); strings.Contains(text, "v0") || strings.Contains(text, "plan v4") {
			t.Fatalf("header advertises a plan version for an empty plan:\n%s", text)
		}
	}
}
