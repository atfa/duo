package gotui

import (
	"fmt"
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
	// Enough rows that the hits cannot all be visible at once: jumping must move
	// the viewport, which is what Esc has to put back. The three matched messages
	// are Chinese, so the wrap and the wide-rune columns are exercised together.
	texts := make([]string, 40)
	for i := range texts {
		texts[i] = fmt.Sprintf("第%d条：普通内容，没有命中", i)
	}
	texts[2] = "第一行：世界和平"
	texts[20] = "第二行：你好世界"
	texts[38] = "第三行：世界依然和平"
	fake.history = searchEntries(texts...)

	app := New(fake)
	app.showTimestamp = false
	placeApp(t, app, 80, 24)

	opened := viewport{offset: app.timeline.offset, follow: app.timeline.follow}
	if !opened.follow {
		t.Fatalf("a fresh transcript should be following the bottom: %+v", opened)
	}

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
	if app.search.current != 0 || app.timeline.searchHit != 0 {
		t.Fatalf("current hit = %d (highlight %d), want 0", app.search.current, app.timeline.searchHit)
	}
	if app.timeline.offset >= opened.offset || app.timeline.follow {
		t.Fatalf("revealing the first hit left offset %d follow %t, want a scrolled viewport below offset %d",
			app.timeline.offset, app.timeline.follow, opened.offset)
	}

	press := func(key tcell.Key, str string, mod tcell.ModMask) {
		t.Helper()
		app.handleKey(tcell.NewEventKey(key, str, mod))
	}
	press(tcell.KeyRune, "n", tcell.ModNone)
	if app.search.current != 1 || app.timeline.searchHit != 1 {
		t.Fatalf("after n current = %d (highlight %d), want 1", app.search.current, app.timeline.searchHit)
	}
	press(tcell.KeyRune, "n", tcell.ModNone)
	press(tcell.KeyRune, "n", tcell.ModNone)
	if app.search.current != 0 {
		t.Fatalf("after wrapping forward current = %d, want 0", app.search.current)
	}
	// A real terminal reports Shift+N with ModShift, so the nav key must not
	// require an empty modifier set.
	press(tcell.KeyRune, "N", tcell.ModShift)
	if app.search.current != 2 {
		t.Fatalf("after N (wrapping backwards) current = %d, want 2", app.search.current)
	}

	press(tcell.KeyEsc, "", tcell.ModNone)
	if app.search.open() {
		t.Fatalf("Esc left the search open: %+v", app.search)
	}
	if app.timeline.offset != opened.offset || app.timeline.follow != opened.follow {
		t.Fatalf("Esc restored offset %d follow %t, want offset %d follow %t",
			app.timeline.offset, app.timeline.follow, opened.offset, opened.follow)
	}
	if len(app.timeline.searchHits) != 0 {
		t.Fatalf("Esc left %d highlights behind", len(app.timeline.searchHits))
	}

	// Typing after the search ended must reach the composer, not jump.
	press(tcell.KeyRune, "n", tcell.ModNone)
	if !strings.Contains(app.composer.Text(), "n") {
		t.Fatalf("composer text = %q, want the typed n after the search ended", app.composer.Text())
	}
}

// TestSyncSearchRecomputesWhenWindowSlides covers the case the reflow check alone
// misses: the window evicts an entry and appends another, so the line count stays
// the same while every hit's line index moves.
func TestSyncSearchRecomputesWhenWindowSlides(t *testing.T) {
	v := newTranscript(newTheme())
	v.SetRect(0, 0, 60, 10)
	v.SetEntries([]entry{
		{seq: 1, pane: "Austin", text: "beta"},
		{seq: 2, pane: "Austin", text: "x"},
	}, false)
	app := &App{}
	app.search = searchState{target: v, query: "beta", nav: true}
	app.refreshSearch(false)
	if len(app.search.hits) != 1 || app.search.hits[0].line != 1 {
		t.Fatalf("hits = %+v, want the body line of the first entry", app.search.hits)
	}

	// Same rendered line count, different window.
	v.SetEntries([]entry{
		{seq: 2, pane: "Austin", text: "y"},
		{seq: 3, pane: "Austin", text: "beta"},
	}, false)
	if len(v.lines) != 5 {
		t.Fatalf("lines = %d, want the same 5 as before", len(v.lines))
	}
	app.syncSearch()
	if len(app.search.hits) != 1 || app.search.hits[0].line != 4 {
		t.Fatalf("hits = %+v, want the body line of the second entry (4)", app.search.hits)
	}
}
