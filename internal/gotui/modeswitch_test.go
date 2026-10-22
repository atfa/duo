package gotui

import (
	"strings"
	"testing"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/session"
	"github.com/gdamore/tcell/v3"
)

func newModeSwitchApp(t *testing.T, snap project.Snapshot) (*fakeSession, *App) {
	t.Helper()
	fake := newFakeSession()
	fake.view.Project = snap
	return fake, New(fake)
}

func switchCommands(fake *fakeSession) []session.SwitchMode {
	var out []session.SwitchMode
	for _, command := range fake.snapshotCommands() {
		if set, ok := command.(session.SwitchMode); ok {
			out = append(out, set)
		}
	}
	return out
}

func confirmText(app *App) string {
	if app.confirm == nil {
		return ""
	}
	return app.confirm.title + "\n" + app.confirm.text
}

func TestModeSwitchConfirmNamesWhatIsDiscarded(t *testing.T) {
	snap := project.Snapshot{
		Mode:        project.ModeStep,
		Phase:       project.PhaseExecute,
		Plan:        "# Step Plan\n\nwork",
		PlanVersion: 3,
		Steps:       project.Steps{{Title: "one", Done: true}, {Title: "two"}},
		Ready:       map[protocol.AgentID]bool{protocol.Austin: true},
		Started:     true,
	}
	fake, app := newModeSwitchApp(t, snap)

	app.runSlash("/mode fast")
	if app.overlay != overlayConfirm {
		t.Fatalf("overlay = %v, want the discard confirmation", app.overlay)
	}
	text := confirmText(app)
	for _, want := range []string{
		"Switch to FAST?",
		"the Step queue (2 steps, 1 landed)",
		"the shared plan v3",
		"signatures from Austin",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("confirmation missing %q:\n%s", want, text)
		}
	}
	if app.confirm.choice2 != "Keep current mode" || app.confirm.choice1 != "Switch mode" {
		t.Fatalf("buttons = %q / %q, want keep / switch", app.confirm.choice2, app.confirm.choice1)
	}

	// Esc cancels: no command at all.
	app.handleConfirmKey(tcell.NewEventKey(tcell.KeyEsc, "", tcell.ModNone))
	if app.overlay != overlayNone {
		t.Fatalf("overlay after Esc = %v, want none", app.overlay)
	}
	if got := len(switchCommands(fake)); got != 0 {
		t.Fatalf("Esc sent %d SwitchMode command(s)", got)
	}

	// Enter on the default (left) button keeps the current mode.
	app.runSlash("/mode fast")
	app.handleConfirmKey(tcell.NewEventKey(tcell.KeyEnter, "", tcell.ModNone))
	if got := len(switchCommands(fake)); got != 0 {
		t.Fatalf("default Enter sent %d SwitchMode command(s)", got)
	}

	// Confirming sends exactly one quiet switch.
	app.runSlash("/mode fast")
	app.handleConfirmKey(tcell.NewEventKey(tcell.KeyRight, "", tcell.ModNone))
	app.handleConfirmKey(tcell.NewEventKey(tcell.KeyEnter, "", tcell.ModNone))
	commands := switchCommands(fake)
	if len(commands) != 1 {
		t.Fatalf("confirmed switch sent %d SwitchMode command(s), want 1", len(commands))
	}
	if commands[0].Mode != "fast" || !commands[0].Quiet {
		t.Fatalf("confirmed switch = %+v, want fast and quiet", commands[0])
	}
}

// A Step queue and plan are lost on STEP → GOAL too, not only on a switch to
// FAST; the dialog must name both.
func TestModeSwitchFromStepToGoalNamesQueueAndPlan(t *testing.T) {
	snap := project.Snapshot{
		Mode:        project.ModeStep,
		Phase:       project.PhasePlan,
		Plan:        "# Step Plan",
		PlanVersion: 2,
		Steps:       project.Steps{{Title: "one"}, {Title: "two"}},
		Started:     true,
	}
	_, app := newModeSwitchApp(t, snap)
	app.runSlash("/mode goal")
	text := confirmText(app)
	for _, want := range []string{"Step queue (2 steps, 0 landed)", "the shared plan v2"} {
		if !strings.Contains(text, want) {
			t.Fatalf("confirmation missing %q:\n%s", want, text)
		}
	}
}

// /escalate carries its reason through the confirmation into the one command.
func TestEscalateReasonSurvivesConfirmation(t *testing.T) {
	snap := project.Snapshot{
		Mode:        project.ModeStep,
		Phase:       project.PhasePlan,
		Plan:        "# Step Plan",
		PlanVersion: 1,
		Steps:       project.Steps{{Title: "one"}},
		Started:     true,
	}
	fake, app := newModeSwitchApp(t, snap)
	app.runSlash("/escalate needs a shared design")
	if app.overlay != overlayConfirm {
		t.Fatalf("overlay = %v, want the discard confirmation", app.overlay)
	}
	app.handleConfirmKey(tcell.NewEventKey(tcell.KeyRight, "", tcell.ModNone))
	app.handleConfirmKey(tcell.NewEventKey(tcell.KeyEnter, "", tcell.ModNone))
	commands := switchCommands(fake)
	if len(commands) != 1 {
		t.Fatalf("escalate sent %d SwitchMode command(s), want 1", len(commands))
	}
	if commands[0].Mode != "goal" || commands[0].Reason != "needs a shared design" || !commands[0].Quiet {
		t.Fatalf("escalate command = %+v, want goal with its reason and quiet", commands[0])
	}
}

// Fast → Goal escalation keeps the round, so it never asks. A finished round is
// kept as well; switching there only chooses the next task's mode.
func TestModeSwitchDoesNotAskForEscalationOrDone(t *testing.T) {
	escalating := project.Snapshot{
		Mode:    project.ModeFast,
		Phase:   project.PhaseRunning,
		Started: true,
	}
	fake, app := newModeSwitchApp(t, escalating)
	app.runSlash("/mode goal")
	if strings.Contains(confirmText(app), "Switch to GOAL?") {
		t.Fatal("Fast → Goal escalation asked about discarded work")
	}
	if got := len(switchCommands(fake)); got != 1 {
		t.Fatalf("escalation sent %d SwitchMode command(s), want 1", got)
	}

	done := project.Snapshot{
		Mode:        project.ModeGoal,
		Phase:       project.PhaseDone,
		Plan:        "# Finished",
		PlanVersion: 4,
		Ready:       map[protocol.AgentID]bool{protocol.Austin: true, protocol.Tony: true},
		Started:     true,
	}
	doneFake, doneApp := newModeSwitchApp(t, done)
	doneApp.runSlash("/mode fast")
	if strings.Contains(confirmText(doneApp), "Switch to FAST?") {
		t.Fatal("a DONE round asked about discarded work")
	}
	if got := len(switchCommands(doneFake)); got != 1 {
		t.Fatalf("DONE switch sent %d SwitchMode command(s), want 1", got)
	}

	// A Fast round without verification evidence or a signature has nothing to
	// lose, so even a switch to STEP is immediate.
	plain := project.Snapshot{Mode: project.ModeFast, Phase: project.PhaseRunning, Started: true}
	plainFake, plainApp := newModeSwitchApp(t, plain)
	plainApp.runSlash("/mode step")
	if strings.Contains(confirmText(plainApp), "Switch to STEP?") {
		t.Fatal("an empty Fast round asked about discarded work")
	}
	if got := len(switchCommands(plainFake)); got != 1 {
		t.Fatalf("plain Fast switch sent %d SwitchMode command(s), want 1", got)
	}
}

func TestModeSwitchWarnsAboutPlanVersionAndVerification(t *testing.T) {
	goal := project.Snapshot{
		Mode:        project.ModeGoal,
		Phase:       project.PhasePlan,
		Plan:        "# Goal Plan",
		PlanVersion: 7,
		Started:     true,
	}
	_, app := newModeSwitchApp(t, goal)
	app.runSlash("/mode step")
	if text := confirmText(app); !strings.Contains(text, "the shared plan v7") {
		t.Fatalf("confirmation missing the plan version:\n%s", text)
	}

	verified := project.Snapshot{
		Mode:         project.ModeFast,
		Phase:        project.PhaseVerify,
		Started:      true,
		Verification: project.Verification{Head: "abc123"},
	}
	_, verifyApp := newModeSwitchApp(t, verified)
	verifyApp.runSlash("/mode step")
	if text := confirmText(verifyApp); !strings.Contains(text, "Tony's verification result") {
		t.Fatalf("confirmation missing the verification result:\n%s", text)
	}
}

// The same confirmation speaks Chinese when the interface does.
func TestModeSwitchConfirmationIsTranslated(t *testing.T) {
	fake := newFakeSession()
	fake.view.Language = "zh"
	fake.view.Project = project.Snapshot{
		Mode:        project.ModeStep,
		Phase:       project.PhasePlan,
		Plan:        "# Step Plan",
		PlanVersion: 2,
		Steps:       project.Steps{{Title: "one"}},
		Started:     true,
	}
	app := New(fake)
	app.runSlash("/mode fast")
	text := confirmText(app)
	for _, want := range []string{"切换到 FAST 模式？", "Step 队列（1 步，0 步已落地）", "共享计划 v2", "保留当前模式"} {
		if !strings.Contains(text, want) {
			t.Fatalf("Chinese confirmation missing %q:\n%s", want, text)
		}
	}
}
