package tui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/session"
	"github.com/atfa/duo/internal/terminal"
	"github.com/atfa/duo/internal/workspace"
)

func testApp(w, h int) *App {
	app := &App{
		svc: &fakeSession{},
		live: session.View{
			Version: "v0.4.6",
			Project: project.NewState().Snapshot(),
		},
		width:   w,
		height:  h,
		version: "v0.4.6",
	}
	app.live.Mode = app.live.Project.EffectiveMode()
	app.renderer = newRenderer(frameInterval, nil, app.buildFrame)
	return app
}

// ansiPattern strips CSI styling and OSC 8 hyperlinks, so a test can measure
// the visible text of a frame.
var ansiPattern = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b\[[0-9;?]*[ -/]*[@-~]`)

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

func TestPaneMarkdownStylesWithoutBreakingFrameWidth(t *testing.T) {
	app := testApp(100, 30)
	app.austin = []entry{{text: "# 标题\n> **bold** and *italic* with `code`\n- [docs](https://example.com)\n1. first\n---\n```\n中文 code\n```"}}
	frame := app.buildFrame(renderNormal)
	if !strings.Contains(frame, ansiBold) || !strings.Contains(frame, ansiItalic) || !strings.Contains(frame, ansiCode) || !strings.Contains(frame, ansiLink) {
		t.Fatalf("markdown styles missing from frame: %q", frame)
	}
	plain := ansiPattern.ReplaceAllString(frame, "")
	for i, line := range strings.Split(plain, "\r\n") {
		if got := displayWidth(line); got > 100 {
			t.Fatalf("markdown line %d is %d columns: %q", i, got, line)
		}
	}
	for _, want := range []string{"标题", "│ bold and italic with code", "• docs <https://example.com>", "1. first", "中文 code"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("markdown content %q missing from frame: %q", want, plain)
		}
	}
}

func TestMarkdownListWrapsWithHangingIndent(t *testing.T) {
	var lines []paneLine
	for _, line := range markdownLines("- abcdefghijkl", 10) {
		lines = append(lines, wrapMarkdown(line, 10)...)
	}
	if len(lines) != 2 {
		t.Fatalf("wrapped list lines = %d, want 2", len(lines))
	}
	if got, want := lines[0].text(), "  • abcdef"; got != want {
		t.Fatalf("first list line = %q, want %q", got, want)
	}
	if got, want := lines[1].text(), "    ghijkl"; got != want {
		t.Fatalf("continuation = %q, want %q", got, want)
	}
	app := testApp(60, 30)
	app.austin = []entry{{text: "- abcdefghijklmnopqrstuvwxyz"}}
	for i, line := range visibleLines(app.buildFrame(renderNormal)) {
		if got := displayWidth(line); got > 60 {
			t.Fatalf("frame line %d is %d columns: %q", i, got, line)
		}
	}
}

func TestPaneMarkdownTableFitsAndAligns(t *testing.T) {
	app := testApp(60, 30)
	app.austin = []entry{{text: "| 名称 | 数量 | 备注 |\n| :--- | ---: | :---: |\n| 苹果 | 12 | fresh |\n| 香蕉 | 3 | 长文本会截断 |"}}
	frame := app.buildFrame(renderNormal)
	if !strings.Contains(frame, ansiBold) {
		t.Fatal("table header is not emphasized")
	}
	plain := ansiPattern.ReplaceAllString(frame, "")
	for i, line := range strings.Split(plain, "\r\n") {
		if got := displayWidth(line); got > 60 {
			t.Fatalf("table line %d is %d columns: %q", i, got, line)
		}
	}
	for _, want := range []string{"┌", "┬", "┼", "┘", "│名称", "│  12", "苹果"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("table content %q missing from frame: %q", want, plain)
		}
	}
}

func TestMarkdownTableWrapsNarrowCellsWithoutDroppingContent(t *testing.T) {
	lines := markdownLines("| Description |\n| --- |\n| complete content remains visible |", 13)
	var text strings.Builder
	for _, line := range lines {
		if got := displayWidth(line.text()); got != 13 {
			t.Fatalf("table line is %d columns, want 13: %q", got, line.text())
		}
		text.WriteString(line.text())
		text.WriteByte('\n')
	}
	got := text.String()
	for _, want := range []string{"┌", "├", "│", "└"} {
		if !strings.Contains(got, want) {
			t.Fatalf("narrow table is missing border %q: %q", want, got)
		}
	}
	content := strings.NewReplacer("┌", "", "┐", "", "├", "", "┤", "", "└", "", "┘", "", "│", "", "─", "", " ", "", "\n", "").Replace(got)
	if !strings.Contains(content, "completecontentremainsvisible") {
		t.Fatalf("narrow table lost content: %q", got)
	}
}

func TestMarkdownTableNormalizesInlineCellText(t *testing.T) {
	lines := markdownLines("| Task |\n| --- |\n| **A** |", 20)
	var text strings.Builder
	for _, line := range lines {
		text.WriteString(line.text())
	}
	if got := text.String(); !strings.Contains(got, "│A") || strings.Contains(got, "*") {
		t.Fatalf("table cell did not normalize inline markdown: %q", got)
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

	app.setRuntime(protocol.Austin, harness.AgentRuntime{Busy: true})
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
	if !strings.Contains(frame, "Enter Send · Ctrl/Shift+Enter Newline · Ctrl+A/T Native · Ctrl+/ Help · Ctrl+Q Quit") {
		t.Fatal("minimal footer is missing")
	}
}

func TestMultilineComposerFrameAndCursor(t *testing.T) {
	app := testApp(60, 18)
	app.input = []byte("first\n中文 second\nthird\nfourth\nfifth")
	app.inputPos = len(app.input)
	frame := app.buildFrame(renderNormal)
	lines := visibleLines(frame)
	if len(lines) > 18 {
		t.Fatalf("multiline frame has %d lines", len(lines))
	}
	for i, line := range lines {
		if displayWidth(line) > 60 {
			t.Fatalf("line %d exceeds width: %q", i, line)
		}
	}
	if !strings.Contains(ansiPattern.ReplaceAllString(frame, ""), "fifth") {
		t.Fatal("composer viewport omitted cursor line")
	}
	row, col := app.composerCursor(60, 18)
	if row < 1 || row > 17 || col < 1 || col > 60 {
		t.Fatalf("cursor = %d,%d", row, col)
	}
	app.inputPos = len([]byte("first\n中"))
	row, col = app.composerCursor(60, 18)
	if row < 1 || col < 1 || col > 60 {
		t.Fatalf("unicode cursor = %d,%d", row, col)
	}
	app.width, app.height = 100, 30
	app.clampOffsets()
	if app.composerLayout(100).rows != 4 {
		t.Fatal("composer rows should remain capped at four")
	}
}

// stubWorkspace exposes a fixed Set; the header never calls the other methods.
type stubWorkspace struct {
	workspace.Manager
	set workspace.Set
}

func (s stubWorkspace) Set() workspace.Set { return s.set }

// The top row names the Git repository Duo resolved, not the launch directory or
// the two agent pane titles, and fills the rest with the border dash. The native
// attach buttons now live only in the preview band header.
func TestRepoTitleReplacesPaneHeaders(t *testing.T) {
	a := testApp(100, 30)
	a.timeline = true
	a.live.Worktrees = workspace.Set{Repository: "/tmp/duo-repo"}

	lines := visibleLines(a.buildFrame(renderNormal))
	top := lines[0]
	if !strings.HasPrefix(top, "┌ [GOAL] /tmp/duo-repo ") || !strings.HasSuffix(top, "┐") {
		t.Fatalf("top row = %q, want the repository title", top)
	}
	if !strings.Contains(top, "────────") {
		t.Fatalf("top row is not filled with the border dash: %q", top)
	}
	for _, unwanted := range []string{"Austin", "Tony", "idle", "[↗]"} {
		if strings.Contains(top, unwanted) {
			t.Fatalf("top row still carries %q: %q", unwanted, top)
		}
	}

	// A click on the title row no longer attaches; the band header still does.
	if got := a.hitNativeButton(4, 1); got != "" {
		t.Fatalf("top row click = %q, want no native button", got)
	}
	previewRow := a.layout().content + 3
	if got := a.hitNativeButton(4, previewRow); got != protocol.Austin {
		t.Fatalf("preview header click = %q, want Austin", got)
	}
	if got := a.hitNativeButton(a.width-2, previewRow); got != protocol.Tony {
		t.Fatalf("preview header click = %q, want Tony", got)
	}
}

// The workflow mode has to be readable before any timeline line is: a bold
// [FAST]/[GOAL] badge leads the top row, and the frame lines are tinted to match
// it — cyan for Fast, magenta for Goal — so the colours alone say which mode is
// running.
func TestTopRowBadgesModeAndTintsFrameByMode(t *testing.T) {
	for _, timeline := range []bool{false, true} {
		a := testApp(100, 30)
		a.timeline = timeline
		a.live.Worktrees = workspace.Set{Repository: "/tmp/duo-repo"}

		goal := a.buildFrame(renderNormal)
		if !strings.Contains(goal, ansiTitleGoal+" [GOAL]") {
			t.Fatalf("timeline=%v Goal frame has no bold [GOAL] badge", timeline)
		}
		if !strings.Contains(goal, paint(ansiBorderGoal, "│")) {
			t.Fatalf("timeline=%v Goal frame is not tinted magenta", timeline)
		}
		if strings.Contains(goal, paint(ansiBorderFast, "│")) {
			t.Fatalf("timeline=%v Goal frame still uses the cyan border", timeline)
		}

		a.setProject(project.NewStateFor(project.ModeFast))
		fast := a.buildFrame(renderNormal)
		if !strings.Contains(fast, ansiTitleFast+" [FAST]") {
			t.Fatalf("timeline=%v Fast frame has no bold [FAST] badge", timeline)
		}
		if !strings.Contains(fast, paint(ansiBorderFast, "│")) {
			t.Fatalf("timeline=%v Fast frame is not tinted cyan", timeline)
		}
		if strings.Contains(fast, ansiBorderGoal) {
			t.Fatalf("timeline=%v Fast frame is tinted magenta", timeline)
		}
	}
}

// The badge leads the row, so a narrow terminal clips the repository path rather
// than the mode, and the row still measures exactly the frame width.
func TestTopRowClipsRepositoryBeforeBadge(t *testing.T) {
	a := testApp(70, 24)
	a.timeline = true
	a.live.Worktrees = workspace.Set{Repository: "/a/very/long/repository/path/that/will/not/fit"}

	row := visibleLines(a.buildFrame(renderNormal))[0]
	if !strings.Contains(row, "[GOAL]") {
		t.Fatalf("top row = %q, want the badge to survive clipping", row)
	}
	if got := displayWidth(row); got != 70 {
		t.Fatalf("top row width = %d, want the frame width 70: %q", got, row)
	}
}
