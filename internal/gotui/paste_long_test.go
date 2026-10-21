package gotui

import (
	"image"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/session"
	"github.com/gdamore/tcell/v3"
	ui "github.com/metaspartan/gotui/v5"
)

func TestPasteSuperLongTextCollapsedAndNavigation(t *testing.T) {
	fake := newFakeSession()
	app := New(fake)
	app.composer.SetRect(1, 10, 80, 20)

	longSnippet := "line 1\nline 2\nline 3\nline 4\nline 5"

	// 1. Paste via bracketed paste events
	app.handleEvent(tcell.NewEventPaste(true))
	for _, r := range longSnippet {
		if r == '\n' {
			app.handleKey(tcell.NewEventKey(tcell.KeyEnter, "", tcell.ModNone))
		} else {
			app.handleKey(tcell.NewEventKey(tcell.KeyRune, string(r), tcell.ModNone))
		}
	}
	app.handleEvent(tcell.NewEventPaste(false))

	// Should display as [5 Lines]
	wantLabel := "[5 Lines]"
	if got := app.composer.Text(); got != wantLabel {
		t.Fatalf("composer.Text() = %q, want %q", got, wantLabel)
	}
	if got := app.composer.DisplayText(); got != wantLabel {
		t.Fatalf("composer.DisplayText() = %q, want %q", got, wantLabel)
	}
	if got := string(app.composer.CurrentLine()); got != wantLabel {
		t.Fatalf("composer.CurrentLine() = %q, want %q", got, wantLabel)
	}
	if got := app.composer.ExpandedText(); got != longSnippet {
		t.Fatalf("composer.ExpandedText() = %q, want %q", got, longSnippet)
	}

	// 2. Cursor starts at end of [5 Lines]
	wantCol := len(wantLabel)
	if row, col := app.composer.Cursor(); row != 0 || col != wantCol {
		t.Fatalf("cursor after paste = %d,%d, want 0,%d", row, col, wantCol)
	}

	// 3. Left arrow should treat [5 Lines] as a whole, jumping from end to start
	app.handleKey(tcell.NewEventKey(tcell.KeyLeft, "", tcell.ModNone))
	if row, col := app.composer.Cursor(); row != 0 || col != 0 {
		t.Fatalf("cursor after Left = %d,%d, want 0,0", row, col)
	}

	// 4. Right arrow should treat [5 Lines] as a whole, jumping from start to end
	app.handleKey(tcell.NewEventKey(tcell.KeyRight, "", tcell.ModNone))
	if row, col := app.composer.Cursor(); row != 0 || col != wantCol {
		t.Fatalf("cursor after Right = %d,%d, want 0,%d", row, col, wantCol)
	}

	// 5. Backspace at end should delete the entire [5 Lines] as a whole
	app.handleKey(tcell.NewEventKey(tcell.KeyBackspace2, "", tcell.ModNone))
	if got := app.composer.Text(); got != "" {
		t.Fatalf("composer after Backspace = %q, want empty", got)
	}
	if row, col := app.composer.Cursor(); row != 0 || col != 0 {
		t.Fatalf("cursor after Backspace = %d,%d, want 0,0", row, col)
	}

	// 6. Paste again and test DEL key
	tenLines := strings.Repeat("test line\n", 9) + "last line"
	app.handleEvent(tcell.NewEventPaste(true))
	for _, r := range tenLines {
		if r == '\n' {
			app.handleKey(tcell.NewEventKey(tcell.KeyEnter, "", tcell.ModNone))
		} else {
			app.handleKey(tcell.NewEventKey(tcell.KeyRune, string(r), tcell.ModNone))
		}
	}
	app.handleEvent(tcell.NewEventPaste(false))

	if got := app.composer.Text(); got != "[10 Lines]" {
		t.Fatalf("composer text after 10-line paste = %q, want %q", got, "[10 Lines]")
	}

	// Move cursor to start of [10 Lines]
	app.handleKey(tcell.NewEventKey(tcell.KeyLeft, "", tcell.ModNone))
	if row, col := app.composer.Cursor(); row != 0 || col != 0 {
		t.Fatalf("cursor after Left = %d,%d, want 0,0", row, col)
	}

	// DEL key at start should delete the entire [10 Lines] as a whole
	app.handleKey(tcell.NewEventKey(tcell.KeyDelete, "", tcell.ModNone))
	if got := app.composer.Text(); got != "" {
		t.Fatalf("composer after Delete = %q, want empty", got)
	}
}

func TestInputBatchCoalescesPasteEventsAndDrawsOnPasteEnd(t *testing.T) {
	app := New(newFakeSession())
	draws := 0
	draw := func() { draws++ }

	if stopped := app.handleInputBatch(tcell.NewEventPaste(true), func() (tcell.Event, bool) {
		return nil, false
	}, draw); stopped {
		t.Fatal("paste start unexpectedly stopped app")
	}
	if draws != 0 || !app.pasting {
		t.Fatalf("paste start draws=%d, pasting=%t; want 0 draws and active paste", draws, app.pasting)
	}

	events := []tcell.Event{
		tcell.NewEventKey(tcell.KeyRune, "first line", tcell.ModNone),
		tcell.NewEventKey(tcell.KeyEnter, "", tcell.ModNone),
		tcell.NewEventKey(tcell.KeyRune, "second line", tcell.ModNone),
		tcell.NewEventKey(tcell.KeyEnter, "", tcell.ModNone),
		tcell.NewEventKey(tcell.KeyRune, "third line", tcell.ModNone),
		tcell.NewEventPaste(false),
	}
	index := 0
	next := func() (tcell.Event, bool) {
		if index == len(events) {
			return nil, false
		}
		ev := events[index]
		index++
		return ev, true
	}
	index = 1
	if stopped := app.handleInputBatch(events[0], func() (tcell.Event, bool) {
		return next()
	}, draw); stopped {
		t.Fatal("paste contents unexpectedly stopped app")
	}
	if draws != 1 || app.pasting {
		t.Fatalf("paste batch draws=%d, pasting=%t; want one final draw and closed paste", draws, app.pasting)
	}
	if got, want := app.composer.Text(), "[3 Lines]"; got != want {
		t.Fatalf("composer text = %q, want %q", got, want)
	}
	if got, want := app.composer.ExpandedText(), "first line\nsecond line\nthird line"; got != want {
		t.Fatalf("expanded paste = %q, want %q", got, want)
	}
}

func TestInputBatchIsBoundedAndStopsDraining(t *testing.T) {
	app := New(newFakeSession())
	processed, draws := 0, 0
	app.handleInputBatch(tcell.NewEventKey(tcell.KeyRune, "a", tcell.ModNone), func() (tcell.Event, bool) {
		processed++
		return tcell.NewEventKey(tcell.KeyRune, "b", tcell.ModNone), true
	}, func() { draws++ })
	if processed != maxInputBatch-1 || draws != 1 {
		t.Fatalf("bounded batch processed=%d draws=%d; want %d queued events and one draw", processed, draws, maxInputBatch-1)
	}

	app = New(newFakeSession())
	app.stopApp()
	processed, draws = 0, 0
	if stopped := app.handleInputBatch(tcell.NewEventKey(tcell.KeyRune, "a", tcell.ModNone), func() (tcell.Event, bool) {
		processed++
		return tcell.NewEventKey(tcell.KeyRune, "b", tcell.ModNone), true
	}, func() { draws++ }); !stopped {
		t.Fatal("stopped app continued input handling")
	}
	if processed != 0 || draws != 0 {
		t.Fatalf("stopped batch processed=%d queued events and drew=%d times", processed, draws)
	}
}

func TestPasteSuperLongTextRenderingStyle(t *testing.T) {
	c := newComposer(newTheme())
	c.SetRect(1, 10, 80, 20)

	longSnippet := "alpha\nbeta\ngamma\ndelta"
	c.InsertPaste(longSnippet)

	if got := c.Text(); got != "[4 Lines]" {
		t.Fatalf("composer.Text() = %q, want [4 Lines]", got)
	}

	buf := ui.NewBuffer(image.Rect(0, 0, 80, 25))
	c.Draw(buf)

	// Verify buffer contains "[4 Lines]" and has reverse highlight style
	rows := bufferRows(buf)
	found := false
	for _, row := range rows {
		if strings.Contains(row, "[4 Lines]") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("rendered buffer did not contain [4 Lines]:\n%s", strings.Join(rows, "\n"))
	}

	// Find the cell for '[' of "[4 Lines]" and check style
	labelX := c.Inner.Min.X + 2 // "› " prefix offset
	labelY := c.Inner.Min.Y
	cell := buf.GetCell(image.Pt(labelX, labelY))
	if cell.Rune != '[' {
		t.Fatalf("cell at (%d, %d) = %c, want '['", labelX, labelY, cell.Rune)
	}
	if cell.Style.Modifier&ui.ModifierReverse == 0 {
		t.Fatalf("style modifier does not have ModifierReverse: %v", cell.Style.Modifier)
	}
	if cell.Style.Fg != c.theme.Accent {
		t.Fatalf("style Fg = %v, want Accent %v", cell.Style.Fg, c.theme.Accent)
	}
}

func TestPasteSuperLongTextSubmitExpandsContent(t *testing.T) {
	fake := newFakeSession()
	app := New(fake)
	app.composer.SetRect(1, 10, 80, 20)

	code := "func hello() {\n    println(\"line1\")\n    println(\"line2\")\n}"
	app.handleEvent(tcell.NewEventPaste(true))
	for _, r := range code {
		if r == '\n' {
			app.handleKey(tcell.NewEventKey(tcell.KeyEnter, "", tcell.ModNone))
		} else {
			app.handleKey(tcell.NewEventKey(tcell.KeyRune, string(r), tcell.ModNone))
		}
	}
	app.handleEvent(tcell.NewEventPaste(false))

	if got := app.composer.Text(); got != "[4 Lines]" {
		t.Fatalf("composer text = %q, want [4 Lines]", got)
	}

	// Press Enter to submit
	app.handleKey(tcell.NewEventKey(tcell.KeyEnter, "", tcell.ModNone))

	commands := fake.snapshotCommands()
	if len(commands) != 1 {
		t.Fatalf("commands after Enter = %d, want 1", len(commands))
	}
	submit, ok := commands[0].(session.SubmitTask)
	if !ok {
		t.Fatalf("submitted command is not SubmitTask: %#v", commands[0])
	}
	if submit.Text != code {
		t.Fatalf("submitted text = %q, want original code %q", submit.Text, code)
	}
}

func TestPasteSuperLongTextEditingAroundBlock(t *testing.T) {
	c := newComposer(newTheme())
	c.SetRect(1, 10, 80, 20)

	c.InsertPaste("line 1\nline 2\nline 3\nline 4\nline 5")
	if got := c.Text(); got != "[5 Lines]" {
		t.Fatalf("c.Text() = %q, want [5 Lines]", got)
	}

	// Type suffix
	c.InsertString(" please check")
	if got := c.Text(); got != "[5 Lines] please check" {
		t.Fatalf("c.Text() with suffix = %q", got)
	}

	// Move cursor to start of line and type prefix
	c.Home()
	c.InsertString("prefix: ")
	if got := c.Text(); got != "prefix: [5 Lines] please check" {
		t.Fatalf("c.Text() with prefix = %q", got)
	}

	// Left and Right navigation around the block
	// Current cursor is at len("prefix: ") = 8
	c.Right() // cursor was at start of [5 Lines], should jump over it
	wantCol := len("prefix: [5 Lines]")
	if _, col := c.Cursor(); col != wantCol {
		t.Fatalf("cursor after Right = %d, want %d", col, wantCol)
	}

	c.Left() // cursor was at end of [5 Lines], should jump back to start
	wantCol = len("prefix: ")
	if _, col := c.Cursor(); col != wantCol {
		t.Fatalf("cursor after Left = %d, want %d", col, wantCol)
	}

	// Delete key at start of [5 Lines] removes it entirely
	if !c.Delete() {
		t.Fatal("Delete returned false")
	}
	if got := c.Text(); got != "prefix:  please check" {
		t.Fatalf("c.Text() after Delete = %q, want %q", got, "prefix:  please check")
	}
}

func TestPasteMultipleBlocks(t *testing.T) {
	c := newComposer(newTheme())
	c.SetRect(1, 10, 80, 20)

	c.InsertPaste("a\nb\nc")
	c.InsertString(" and ")
	c.InsertPaste("1\n2\n3\n4\n5")

	if got := c.Text(); got != "[3 Lines] and [5 Lines]" {
		t.Fatalf("c.Text() = %q, want %q", got, "[3 Lines] and [5 Lines]")
	}

	// Backspace at the end should delete [5 Lines]
	if !c.Backspace() {
		t.Fatal("Backspace at end returned false")
	}
	if got := c.Text(); got != "[3 Lines] and " {
		t.Fatalf("c.Text() after first Backspace = %q, want %q", got, "[3 Lines] and ")
	}

	// Backspace 5 times to remove " and "
	for i := 0; i < 5; i++ {
		if !c.Backspace() {
			t.Fatal("Backspace in spaces returned false")
		}
	}
	if got := c.Text(); got != "[3 Lines]" {
		t.Fatalf("c.Text() = %q, want %q", got, "[3 Lines]")
	}

	// Backspace removes [3 Lines]
	if !c.Backspace() {
		t.Fatal("Backspace for first block returned false")
	}
	if got := c.Text(); got != "" {
		t.Fatalf("c.Text() after deleting all = %q, want empty", got)
	}
}

func TestPasteSingleLineSuperLong(t *testing.T) {
	c := newComposer(newTheme())
	c.SetRect(1, 10, 80, 20)

	longSingleLine := strings.Repeat("x", 400)
	c.InsertPaste(longSingleLine)

	if got := c.Text(); got != "[1 Lines]" {
		t.Fatalf("c.Text() = %q, want [1 Lines]", got)
	}
	if got := c.ExpandedText(); got != longSingleLine {
		t.Fatalf("c.ExpandedText() does not match original long single line")
	}
}
