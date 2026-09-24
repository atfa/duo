package tui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/agent"
	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/terminal"
	"github.com/atfa/duo/internal/transport"
)

func testApp(w, h int) *App {
	app := &App{
		state:   project.NewState(),
		tracker: harness.NewTracker(),
		server:  transport.NewServer("127.0.0.1:0", "session", "token"),
		agents:  agent.NewManager(),
		width:   w,
		height:  h,
		version: "v0.4.6",
	}
	app.renderer = newRenderer(frameInterval, nil, app.buildFrame)
	return app
}

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

func visibleLines(frame string) []string {
	return strings.Split(ansiPattern.ReplaceAllString(frame, ""), "\r\n")
}

func TestResizeFrameClearsAndNormalFrameDoesNot(t *testing.T) {
	app := testApp(120, 40)
	if full := app.buildFrame(renderFullClear); !strings.Contains(full, terminal.ClearHome) {
		t.Fatal("full-clear frame is missing ClearHome")
	}
	app.width, app.height = 80, 24
	normal := app.buildFrame(renderNormal)
	if strings.Contains(normal, "\x1b[2J") {
		t.Fatal("normal frame must not clear the screen")
	}
	if !strings.Contains(normal, terminal.Home) {
		t.Fatal("normal frame must reposition the cursor home")
	}
}

func TestTooSmallFrameFitsRealTerminal(t *testing.T) {
	app := testApp(40, 10)
	frame := app.buildFrame(renderFullClear)
	lines := visibleLines(frame)
	if len(lines) > 10 {
		t.Fatalf("too-small frame has %d lines, terminal height is 10", len(lines))
	}
	for i, line := range lines {
		if got := displayWidth(line); got > 40 {
			t.Fatalf("line %d is %d columns wide, terminal width is 40: %q", i, got, line)
		}
	}
	if !strings.Contains(frame, "Terminal too small") {
		t.Fatal("too-small frame is missing the notice")
	}
	if !strings.Contains(frame, "Current: 40×10") {
		t.Fatal("too-small frame should report the real size")
	}
}

func TestNormalFrameFitsRealTerminal(t *testing.T) {
	app := testApp(100, 30)
	lines := visibleLines(app.buildFrame(renderNormal))
	if len(lines) > 30 {
		t.Fatalf("frame has %d lines, terminal height is 30", len(lines))
	}
	for i, line := range lines {
		if got := displayWidth(line); got > 100 {
			t.Fatalf("line %d is %d columns wide, terminal width is 100", i, got)
		}
	}
}

func TestFrameUsesSynchronizedOutput(t *testing.T) {
	app := testApp(100, 30)
	frame := app.buildFrame(renderNormal)
	begin := strings.Index(frame, terminal.BeginSync)
	end := strings.Index(frame, terminal.EndSync)
	if begin != 0 {
		t.Fatalf("frame must start with BeginSync, got index %d", begin)
	}
	if end <= begin {
		t.Fatalf("EndSync(%d) must follow BeginSync(%d)", end, begin)
	}
	if !strings.HasSuffix(frame, terminal.EndSync) {
		t.Fatal("frame must end with EndSync")
	}
	if hide := strings.Index(frame, terminal.HideCursor); hide < begin || hide > end {
		t.Fatal("cursor must be hidden inside the synchronized update")
	}
	// Autowrap is disabled and restored within the same frame write.
	if off, on := strings.Index(frame, terminal.AutoWrapOff), strings.Index(frame, terminal.AutoWrapOn); off < begin || on < off || on > end {
		t.Fatal("autowrap must be disabled and restored inside the frame")
	}
}

type countingWriter struct {
	writes int
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.writes++
	return len(p), nil
}

func TestRendererCoalescesDirtyIntoOneFrame(t *testing.T) {
	writer := &countingWriter{}
	var modes []renderMode
	r := newRenderer(time.Millisecond, writer, func(mode renderMode) string {
		modes = append(modes, mode)
		return "frame"
	})
	if r.dueChan() != nil {
		t.Fatal("idle renderer armed a timer")
	}

	r.markDirty(false)
	r.markDirty(false)
	r.markDirty(true)
	if writer.writes != 0 || r.frames != 0 {
		t.Fatalf("markDirty wrote immediately: writes=%d frames=%d", writer.writes, r.frames)
	}
	if r.dueChan() == nil {
		t.Fatal("markDirty did not schedule a frame")
	}

	if !r.flush() {
		t.Fatal("scheduled frame was not flushed")
	}
	if writer.writes != 1 || r.frames != 1 {
		t.Fatalf("coalesced writes = %d frames = %d, want 1/1", writer.writes, r.frames)
	}
	if len(modes) != 1 || modes[0] != renderFullClear {
		t.Fatalf("coalesced mode = %v, want full clear", modes)
	}
	if r.flush() {
		t.Fatal("idle renderer rendered again")
	}
	if writer.writes != 1 {
		t.Fatalf("extra write after idle flush: %d", writer.writes)
	}
}

func TestIdleSpinnerDoesNotRepaint(t *testing.T) {
	app := testApp(100, 30)
	if app.spinnerActive() {
		t.Fatal("fresh tracker should be idle")
	}
	if app.spinnerTick() {
		t.Fatal("idle spinner tick must not schedule a frame")
	}
	if app.frame != 0 || app.renderer.dirty {
		t.Fatalf("idle tick mutated renderer: frame=%d dirty=%v", app.frame, app.renderer.dirty)
	}

	app.tracker.Handle(protocol.Austin, protocol.ActivityAgentStart)
	if !app.spinnerActive() {
		t.Fatal("busy agent should animate")
	}
	if !app.spinnerTick() {
		t.Fatal("busy spinner tick must schedule a frame")
	}
	if app.frame != 1 || !app.renderer.dirty {
		t.Fatalf("busy tick did not schedule: frame=%d dirty=%v", app.frame, app.renderer.dirty)
	}
	app.view = viewHelp
	app.renderer.dirty = false
	if app.spinnerTick() || app.renderer.dirty {
		t.Fatal("Help spinner tick must not repaint")
	}
}

func TestHelpFrameFitsAndResizeClamps(t *testing.T) {
	app := testApp(60, 18)
	app.view = viewHelp
	app.helpOffset = app.maxHelpOffset()
	lines := visibleLines(app.buildFrame(renderFullClear))
	if len(lines) > 18 {
		t.Fatalf("Help frame has %d lines", len(lines))
	}
	for i, line := range lines {
		if got := displayWidth(line); got > 60 {
			t.Fatalf("Help line %d is %d columns: %q", i, got, line)
		}
	}
	if !strings.Contains(strings.Join(lines, "\n"), "Duo Help · v0.4.6") {
		t.Fatal("Help title is missing dynamic version")
	}
	if strings.Contains(app.buildFrame(renderNormal), terminal.ShowCursor) {
		t.Fatal("Help frame must keep the cursor hidden")
	}
	app.width, app.height = 120, 35
	app.clampHelpOffset()
	if app.helpOffset < 0 || app.helpOffset > app.maxHelpOffset() {
		t.Fatalf("resize offset %d invalid", app.helpOffset)
	}
	resized := visibleLines(app.buildFrame(renderFullClear))
	if len(resized) > 35 {
		t.Fatalf("resized Help frame has %d lines", len(resized))
	}
	for i, line := range resized {
		if got := displayWidth(line); got > 120 {
			t.Fatalf("resized Help line %d is %d columns", i, got)
		}
	}
	for _, line := range app.helpLines(118)[app.helpOffset:minInt(app.helpOffset+app.helpVisibleRows(), len(app.helpLines(118)))] {
		if line != "" {
			if !strings.Contains(strings.Join(resized, "\n"), line) {
				t.Fatalf("resized Help did not render visible content %q", line)
			}
			break
		}
	}
}

func TestMainStatusComposerAndFooter(t *testing.T) {
	app := testApp(100, 30)
	app.status = "Tony restarted"
	frame := ansiPattern.ReplaceAllString(app.buildFrame(renderNormal), "")
	if !strings.Contains(frame, "Status: Tony restarted") || !strings.Contains(frame, "Duo → Austin >") {
		t.Fatalf("status/composer missing: %q", frame)
	}
	if strings.Contains(frame, "Duo → Austin > (Tony restarted)") {
		t.Fatal("status was rendered as composer input")
	}
	if !strings.Contains(frame, "Enter Send · Ctrl+A/T Native · Ctrl+/ Help · Ctrl+Q Quit") {
		t.Fatal("minimal footer is missing")
	}
}
