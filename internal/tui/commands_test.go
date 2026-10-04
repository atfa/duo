package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/project"
)

func newTestApp() *App {
	app := New(&fakeSession{})
	app.setProject(project.NewStateFor(project.ModeFast))
	app.width = 80
	app.height = 24
	return app
}

func TestSlashPrefixAndMatching(t *testing.T) {
	app := newTestApp()

	// 1. Bare slash matches all commands
	app.input = []byte("/")
	app.inputPos = 1
	if app.slashPrefix() != "/" {
		t.Fatalf("slashPrefix() = %q, want \"/\"", app.slashPrefix())
	}
	matches := app.matchingSlashCommands()
	if len(matches) != len(slashCommandList) {
		t.Fatalf("matchingSlashCommands() returned %d commands, want %d", len(matches), len(slashCommandList))
	}
	if !app.hasSlashMenu() {
		t.Fatal("hasSlashMenu() should be true for \"/\"")
	}

	// 2. Typing /e narrows to /escalate
	app.input = []byte("/e")
	app.inputPos = 2
	matches = app.matchingSlashCommands()
	if len(matches) != 1 || matches[0].Name != "/escalate" {
		t.Fatalf("expected 1 match (/escalate), got %+v", matches)
	}

	// 3. Typing /mo matches /mode and /model
	app.input = []byte("/mo")
	app.inputPos = 3
	matches = app.matchingSlashCommands()
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches (/mode, /model), got %d", len(matches))
	}

	// 4. Unknown command has no matches
	app.input = []byte("/nonexistent")
	app.inputPos = len(app.input)
	if app.hasSlashMenu() {
		t.Fatal("hasSlashMenu() should be false for non-matching command")
	}

	// 5. Space after command means typing arguments, menu closes
	app.input = []byte("/escalate ")
	app.inputPos = len(app.input)
	if app.hasSlashMenu() {
		t.Fatal("hasSlashMenu() should be false after space")
	}

	// 6. Mid-sentence slash or comment should not activate menu
	app.input = []byte("hello /world")
	app.inputPos = len(app.input)
	if app.hasSlashMenu() {
		t.Fatal("hasSlashMenu() should be false for mid-sentence slash")
	}

	app.input = []byte("// comment")
	app.inputPos = len(app.input)
	if app.hasSlashMenu() {
		t.Fatal("hasSlashMenu() should be false for // comment")
	}
}

func TestSlashNavigationAndSelection(t *testing.T) {
	app := newTestApp()
	app.input = []byte("/m")
	app.inputPos = 2

	matches := app.matchingSlashCommands() // should be /mode and /model
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(matches))
	}

	// Initially cursor is 0 (/mode)
	if app.slashCursor != 0 {
		t.Fatalf("slashCursor = %d, want 0", app.slashCursor)
	}

	// Down moves to 1 (/model)
	app.slashDown()
	if app.slashCursor != 1 {
		t.Fatalf("after slashDown(), slashCursor = %d, want 1", app.slashCursor)
	}

	// Down again wraps to 0
	app.slashDown()
	if app.slashCursor != 0 {
		t.Fatalf("after slashDown() wrap, slashCursor = %d, want 0", app.slashCursor)
	}

	// Up wraps to 1
	app.slashUp()
	if app.slashCursor != 1 {
		t.Fatalf("after slashUp() wrap, slashCursor = %d, want 1", app.slashCursor)
	}

	// Tab autocompletes /model (needs no args)
	app.autocompleteSlashCommand()
	if string(app.input) != "/model" {
		t.Fatalf("after Tab, input = %q, want \"/model\"", string(app.input))
	}

	// Now test command with needsArgs: /mode
	app.input = []byte("/mod")
	app.inputPos = 4
	app.slashCursor = 0
	needsMore := app.selectSlashCommand()
	if !needsMore {
		t.Fatal("selectSlashCommand for /mode should return true (needsArgs)")
	}
	if string(app.input) != "/mode " {
		t.Fatalf("after Enter on /mode, input = %q, want \"/mode \"", string(app.input))
	}

	// Now test dismiss with Esc
	app.input = []byte("/")
	app.inputPos = 1
	if !app.hasSlashMenu() {
		t.Fatal("menu should be active")
	}
	app.handleKey("esc")
	if app.hasSlashMenu() {
		t.Fatal("menu should be dismissed after Esc")
	}

	// Typing another character un-dismisses
	app.handleKey("e")
	if !app.hasSlashMenu() {
		t.Fatal("menu should reactivate after typing a new character")
	}
}

func TestSlashMenuRendering(t *testing.T) {
	app := newTestApp()
	app.input = []byte("/")
	app.inputPos = 1

	frame := app.buildFrame(renderNormal)
	if !strings.Contains(frame, "Commands") {
		t.Fatalf("frame does not contain slash command menu:\n%s", frame)
	}
	if !strings.Contains(frame, "/escalate") {
		t.Fatalf("frame does not list /escalate:\n%s", frame)
	}
	if !strings.Contains(frame, "▶") {
		t.Fatalf("frame does not have selection indicator ▶:\n%s", frame)
	}

	// Ensure lines do not overflow terminal width
	for idx, line := range strings.Split(frame, "\r\n") {
		w := displayWidth(line)
		// Strip ANSI to check visible width
		plain := ansiPattern.ReplaceAllString(line, "")
		pw := displayWidth(plain)
		if pw > app.width {
			t.Fatalf("line %d display width %d exceeds terminal width %d: %q", idx, pw, app.width, plain)
		}
		_ = w
	}
}

func TestRunSlashCommand(t *testing.T) {
	ctx := context.Background()
	app := newTestApp()

	// 1. /help toggles help view
	if !app.runSlashCommand(ctx, "/help") {
		t.Fatal("/help not handled")
	}
	if app.view != viewHelp {
		t.Fatalf("view = %v, want viewHelp", app.view)
	}
	// toggle back
	if !app.runSlashCommand(ctx, "/help") {
		t.Fatal("/help not handled")
	}
	if app.view != viewMain {
		t.Fatalf("view = %v, want viewMain", app.view)
	}

	// 2. /overview toggles detail view
	if !app.runSlashCommand(ctx, "/overview") {
		t.Fatal("/overview not handled")
	}
	if app.view != viewDetail {
		t.Fatalf("view = %v, want viewDetail", app.view)
	}
	if !app.runSlashCommand(ctx, "/overview") {
		t.Fatal("/overview not handled")
	}
	if app.view != viewMain {
		t.Fatalf("view = %v, want viewMain", app.view)
	}

	// 3. /timestamps toggles timestamps
	origTs := app.showTimestamps
	if !app.runSlashCommand(ctx, "/timestamps") {
		t.Fatal("/timestamps not handled")
	}
	if app.showTimestamps == origTs {
		t.Fatal("showTimestamps was not toggled")
	}

	// 4. /preview toggles preview band
	origPreview := app.hidePreview
	if !app.runSlashCommand(ctx, "/preview") {
		t.Fatal("/preview not handled")
	}
	if app.hidePreview == origPreview {
		t.Fatal("hidePreview was not toggled")
	}

	// 5. /clear clears
	if !app.runSlashCommand(ctx, "/clear") {
		t.Fatal("/clear not handled")
	}

	// 6. /status reports status
	if !app.runSlashCommand(ctx, "/status") {
		t.Fatal("/status not handled")
	}

	// 7. /mode without args reports mode
	if !app.runSlashCommand(ctx, "/mode") {
		t.Fatal("/mode without args not handled")
	}
	if !strings.Contains(app.status, "current mode:") {
		t.Fatalf("status = %q, expected mode report", app.status)
	}

	// 8. /mode goal and /escalate return false so SubmitUserTask handles them
	if app.runSlashCommand(ctx, "/mode goal") {
		t.Fatal("/mode goal should return false to delegate to coordinator")
	}
	if app.runSlashCommand(ctx, "/escalate something") {
		t.Fatal("/escalate should return false to delegate to coordinator")
	}

	// 9. File path starting with / returns false
	if app.runSlashCommand(ctx, "/usr/bin/python error") {
		t.Fatal("/usr/bin/python should return false to be treated as user task")
	}

	// 10. Unknown command reports error in status
	if !app.runSlashCommand(ctx, "/foobar") {
		t.Fatal("unknown slash command should be handled")
	}
	if !app.statusError || !strings.Contains(app.status, "unknown command") {
		t.Fatalf("status = %q (err=%t), want unknown command error", app.status, app.statusError)
	}
}
