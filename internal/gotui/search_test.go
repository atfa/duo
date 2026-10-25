package gotui

import (
	"image"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/sessionstore"
	"github.com/gdamore/tcell/v3"
	ui "github.com/metaspartan/gotui/v5"
)

func searchEntries(texts ...string) []sessionstore.TUIEntry {
	out := make([]sessionstore.TUIEntry, 0, len(texts))
	for i, text := range texts {
		out = append(out, sessionstore.TUIEntry{
			Time:  time.Date(2026, 10, 20, 16, 2, i, 0, time.Local),
			Pane:  "Austin",
			Label: "Austin → Human",
			Text:  text,
		})
	}
	return out
}

// placeApp lays the dashboard out at a fixed size without a terminal, so a test
// can draw one widget into a buffer and inspect the resulting cells.
func placeApp(t *testing.T, app *App, width, height int) {
	t.Helper()
	// The dashboard would otherwise read the developer's saved view preference.
	app.viewMode = viewTimeline
	app.saveViewPref = nil
	app.layout = app.computeLayout(width, height)
	app.updateWidgets()
}

func drawTranscript(t *testing.T, v *transcript, width, height int) *ui.Buffer {
	t.Helper()
	buf := ui.NewBuffer(image.Rect(0, 0, width, height))
	v.Draw(buf)
	return buf
}

func cellAt(t *testing.T, v *transcript, buf *ui.Buffer, line, col int) ui.Cell {
	t.Helper()
	y := v.Inner.Min.Y + line - v.offset
	if y < v.Inner.Min.Y || y >= v.Inner.Max.Y {
		t.Fatalf("line %d is not visible at offset %d", line, v.offset)
	}
	return buf.GetCell(image.Pt(v.Inner.Min.X+col, y))
}

func TestSearchHighlightsMatches(t *testing.T) {
	fake := newFakeSession()
	fake.history = searchEntries("Deploy failed. Retry the DEPLOY once.")
	app := New(fake)
	app.showTimestamp = false
	placeApp(t, app, 80, 24)

	app.openSearch("deploy")
	if len(app.search.hits) != 2 {
		t.Fatalf("hits = %d, want 2 case-insensitive matches", len(app.search.hits))
	}
	if !app.search.nav || app.search.active {
		t.Fatalf("a /search <term> should commit at once: %+v", app.search)
	}

	buf := drawTranscript(t, app.timeline, 80, 24)
	first, second := app.search.hits[0], app.search.hits[1]
	if got := cellAt(t, app.timeline, buf, first.line, first.col).Style.Bg; got != app.theme.Accent {
		t.Fatalf("current hit background = %v, want the accent %v", got, app.theme.Accent)
	}
	if got := cellAt(t, app.timeline, buf, second.line, second.col).Style.Bg; got != app.theme.Warn {
		t.Fatalf("other hit background = %v, want the warning colour %v", got, app.theme.Warn)
	}
	// Text outside the match keeps its body background: the highlight must cover
	// the match and nothing else.
	body := app.timeline.lines[first.line]
	if body.width() > first.end+1 {
		if got := cellAt(t, app.timeline, buf, first.line, first.end+1).Style.Bg; got != app.theme.Bg {
			t.Fatalf("cell after the hit background = %v, want the body %v", got, app.theme.Bg)
		}
	}

	// A text selection outranks the highlight, which is what lets the human copy
	// exactly the marked text.
	app.timeline.StartSelection(app.timeline.Inner.Min.X, app.timeline.Inner.Min.Y)
	app.timeline.DragSelection(app.timeline.Inner.Max.X-1, app.timeline.Inner.Min.Y+2)
	buf = drawTranscript(t, app.timeline, 80, 24)
	if got := cellAt(t, app.timeline, buf, first.line, first.col).Style.Bg; got != app.theme.SelectBg {
		t.Fatalf("selected hit background = %v, want the selection %v", got, app.theme.SelectBg)
	}
}

// TestSearchColumnsUseCells covers the wide-rune case directly: a hit after two
// CJK runes starts at cell 4, not at rune index 2, or the highlight would cover
// the wrong half of the line.
func TestSearchColumnsUseCells(t *testing.T) {
	v := newTranscript(newTheme())
	v.SetRect(0, 0, 40, 12)
	v.SetEntries([]entry{{text: "你好世界和平", pane: "Austin", seq: 1}}, false)

	hits := v.searchMatches("和平")
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(hits))
	}
	// Two wide runes before the hit occupy four cells, plus the two-cell body
	// indent every transcript line carries.
	if hits[0].col != 10 || hits[0].end != 14 {
		t.Fatalf("hit columns = %d..%d, want 10..14 (cells, not rune indexes)", hits[0].col, hits[0].end)
	}
}

func TestSearchNextWrapsAround(t *testing.T) {
	fake := newFakeSession()
	fake.history = searchEntries(
		"第一行：世界和平",
		"no match here",
		"第二行：你好世界",
		"第三行：世界依然和平",
	)
	app := New(fake)
	app.showTimestamp = false
	placeApp(t, app, 80, 24)

	app.handleKey(tcell.NewEventKey(tcell.KeyCtrlF, "", tcell.ModNone))
	if !app.search.active {
		t.Fatal("Ctrl+F did not open the search prompt")
	}
	for _, r := range "世界" {
		app.handleKey(tcell.NewEventKey(tcell.KeyRune, string(r), tcell.ModNone))
	}
	app.handleKey(tcell.NewEventKey(tcell.KeyEnter, "", tcell.ModNone))
	if app.search.active || !app.search.nav {
		t.Fatalf("Enter should commit the search: %+v", app.search)
	}
	if len(app.search.hits) != 3 {
		t.Fatalf("hits = %d, want 3 (matches on three messages)", len(app.search.hits))
	}
	if app.search.current != 0 {
		t.Fatalf("current hit = %d, want 0", app.search.current)
	}
	if got := app.timeline.searchHit; got != 0 {
		t.Fatalf("highlighted hit = %d, want 0", got)
	}

	n := func(key tcell.Key, str string) {
		app.handleKey(tcell.NewEventKey(key, str, tcell.ModNone))
	}
	n(tcell.KeyRune, "n")
	if app.search.current != 1 {
		t.Fatalf("current hit after n = %d, want 1", app.search.current)
	}
	n(tcell.KeyRune, "n")
	n(tcell.KeyRune, "n")
	if app.search.current != 0 {
		t.Fatalf("current hit after wrapping = %d, want 0", app.search.current)
	}
	n(tcell.KeyRune, "N")
	if app.search.current != 2 {
		t.Fatalf("current hit after N wraps backwards = %d, want 2", app.search.current)
	}

	// Esc ends the search and returns the transcript to where it was.
	offset, follow := app.search.saved.offset, app.search.saved.follow
	n(tcell.KeyEsc, "")
	if app.search.open() {
		t.Fatalf("Esc left the search open: %+v", app.search)
	}
	if app.timeline.offset != offset || app.timeline.follow != follow {
		t.Fatalf("viewport = offset %d follow %t, want offset %d follow %t",
			app.timeline.offset, app.timeline.follow, offset, follow)
	}
	if len(app.timeline.searchHits) != 0 {
		t.Fatalf("Esc left %d highlights behind", len(app.timeline.searchHits))
	}

	// Typing while the search is not committed any more must reach the composer.
	n(tcell.KeyRune, "n")
	if !strings.Contains(app.composer.Text(), "n") {
		t.Fatalf("composer text = %q, want the typed n after the search ended", app.composer.Text())
	}
}
