package gotui

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/gdamore/tcell/v3"
	rw "github.com/mattn/go-runewidth"
	ui "github.com/metaspartan/gotui/v5"
)

// searchHit is one match inside the rendered transcript, in cell columns of one
// render line. Columns rather than byte or rune offsets are what Draw needs, and
// they are how a wide CJK rune still highlights exactly its own cells.
type searchHit struct {
	line int
	col  int
	end  int
}

// viewport is the scroll state a cancelled search restores.
type viewport struct {
	offset int
	follow bool
}

// searchState is the transcript search: an open prompt, or the committed
// navigation state where n/N jump and Esc ends. It targets one pane — the one
// that was active when the search opened — and it never edits the transcript,
// so a search cannot change what the session recorded.
type searchState struct {
	active  bool
	nav     bool
	query   string
	hits    []searchHit
	current int
	target  *transcript
	saved   viewport

	// hitsWidth and hitsLines describe the transcript when hits were computed.
	// The transcript reflows on a width change and drops window-evicted rows, so
	// both invalidate the line indexes the hits carry.
	hitsWidth   int
	hitsLines   int
	hitsFirst   int
	hitsEntries int
}

func (s searchState) open() bool { return s.active || s.nav }

// searchStyle highlights a match. Both colours come from the existing palette,
// and the current hit is the accent so the one Enter or n selected is visible
// among the others.
func (t theme) searchStyle(current bool) ui.Style {
	if current {
		return ui.Style{Fg: t.Bg, Bg: t.Accent, Modifier: ui.ModifierBold}
	}
	return ui.Style{Fg: t.Bg, Bg: t.Warn}
}

// searchMatches finds every case-insensitive occurrence of query in the
// rendered lines. Hits come out line-major and ordered, which is what Draw's
// per-line scan relies on.
func (v *transcript) searchMatches(query string) []searchHit {
	needle := make([]rune, 0, len(query))
	for _, r := range query {
		// Per-rune folding keeps needle and haystack aligned; a few characters
		// change rune count under strings.ToLower ("İ" becomes two runes).
		needle = append(needle, unicode.ToLower(r))
	}
	if len(needle) == 0 {
		return nil
	}
	var hits []searchHit
	for idx, line := range v.lines {
		runes := []rune(line.text())
		if len(runes) < len(needle) {
			continue
		}
		// columns[i] is the cell column where rune i starts.
		columns := make([]int, len(runes)+1)
		col := 0
		for i, r := range runes {
			columns[i] = col
			w := rw.RuneWidth(r)
			if w > 0 {
				col += w
			}
		}
		columns[len(runes)] = col
		for i := 0; i+len(needle) <= len(runes); i++ {
			match := true
			for j := range needle {
				if unicode.ToLower(runes[i+j]) != needle[j] {
					match = false
					break
				}
			}
			if match {
				hits = append(hits, searchHit{line: idx, col: columns[i], end: columns[i+len(needle)]})
			}
		}
	}
	return hits
}

// searchHitsOnLine returns the hits of one rendered line and the index of the
// first of them in the full hit list, so Draw can tell which one is current.
func (v *transcript) searchHitsOnLine(line int) ([]searchHit, int) {
	base := sort.Search(len(v.searchHits), func(i int) bool { return v.searchHits[i].line >= line })
	if base >= len(v.searchHits) || v.searchHits[base].line != line {
		return nil, base
	}
	end := base
	for end < len(v.searchHits) && v.searchHits[end].line == line {
		end++
	}
	return v.searchHits[base:end], base
}

// entriesStamp identifies the window the hits were computed against. The first
// entry's seq and the entry count change when the window evicts while appending,
// even when the reflowed line count happens to land on the same number — a case
// that would otherwise leave every hit pointing one entry too far back.
func (v *transcript) entriesStamp() (int, int) {
	first := 0
	if len(v.entries) > 0 {
		first = v.entries[0].seq
	}
	return first, len(v.entries)
}

// SetSearchHits replaces what the transcript highlights. current indexes
// hits, so the pane can emphasise the hit Enter or n selected.
func (v *transcript) SetSearchHits(hits []searchHit, current int) {
	v.searchHits, v.searchHit = hits, current
}

func (v *transcript) ClearSearchHits() {
	v.searchHits, v.searchHit = nil, 0
}

// revealLine scrolls a hit into view. Scrolling up to look at a hit is not
// following new output, so the transcript stops tracking the bottom; the
// cancelled search restores whatever it was before.
func (v *transcript) revealLine(line int) {
	height := v.Inner.Dy()
	if height <= 0 || line < 0 || line >= len(v.lines) {
		return
	}
	if line >= v.offset && line < v.offset+height {
		return
	}
	offset := line - height/3
	if offset < 0 {
		offset = 0
	}
	if maxOffset := max(0, len(v.lines)-height); offset > maxOffset {
		offset = maxOffset
	}
	v.offset = offset
	v.follow = false
	v.newLines = 0
	v.clampOffset()
}

// openSearch starts a search on the active pane. An empty term opens the
// prompt; a term given as an argument (or /search <term>) searches at once and
// leaves n/N active.
func (a *App) openSearch(term string) {
	target := a.activeTranscript()
	a.search = searchState{target: target, saved: viewport{offset: target.offset, follow: target.follow}}
	term = strings.TrimSpace(term)
	if term == "" {
		a.search.active = true
		a.setStatus("", false)
		return
	}
	a.search.query = term
	a.refreshSearch(true)
	a.search.nav = true
	a.setStatus(a.searchStatus(), false)
}

// refreshSearch recomputes the hits for the current query. It runs when the
// query changes, when the human jumps and when new output reflows the lines, so
// the highlighted rows can never drift from the text they matched.
func (a *App) refreshSearch(reveal bool) {
	st := &a.search
	if st.target == nil {
		return
	}
	st.hits = st.target.searchMatches(st.query)
	st.hitsWidth, st.hitsLines = st.target.width, len(st.target.lines)
	st.hitsFirst, st.hitsEntries = st.target.entriesStamp()
	st.current = 0
	if len(st.hits) == 0 {
		st.target.ClearSearchHits()
		return
	}
	st.target.SetSearchHits(st.hits, st.current)
	if reveal {
		st.target.revealLine(st.hits[st.current].line)
	}
}

// syncSearch keeps hits valid while the transcript reflows or receives output:
// the line indexes the hits carry are only meaningful for the exact lines that
// were searched. It does not scroll, because the human may be reading elsewhere.
func (a *App) syncSearch() {
	if !a.search.open() || a.search.target == nil || a.search.query == "" {
		return
	}
	target := a.search.target
	first, count := target.entriesStamp()
	if target.width == a.search.hitsWidth && len(target.lines) == a.search.hitsLines &&
		first == a.search.hitsFirst && count == a.search.hitsEntries {
		return
	}
	a.refreshSearch(false)
}

// searchJump moves to the next (or previous) hit and wraps around, which is
// what makes a long session's hits reachable without counting them.
func (a *App) searchJump(delta int) {
	st := &a.search
	if len(st.hits) == 0 || st.target == nil {
		a.setStatus(a.searchStatus(), false)
		return
	}
	st.current = (st.current + delta + len(st.hits)) % len(st.hits)
	st.target.SetSearchHits(st.hits, st.current)
	st.target.revealLine(st.hits[st.current].line)
	a.setStatus(a.searchStatus(), false)
}

// closeSearch ends the search. restore puts the viewport back where it was
// before the search opened (Esc); without it the view stays on the hit the
// human navigated to and only the highlight goes away.
func (a *App) closeSearch(restore bool) {
	if a.search.target != nil {
		a.search.target.ClearSearchHits()
		if restore {
			a.search.target.offset = a.search.saved.offset
			a.search.target.follow = a.search.saved.follow
			a.search.target.clampOffset()
		}
	}
	a.search = searchState{}
	a.setStatus("", false)
}

func (a *App) searchStatus() string {
	if len(a.search.hits) == 0 {
		return a.theme.tr("no matches")
	}
	return fmt.Sprintf("%d/%d %s", a.search.current+1, len(a.search.hits), a.theme.tr("matches"))
}

// searchHint is the footer line of an open search. The prompt shows the query
// being typed; the committed state shows the position and the n/N keys.
func (a *App) searchHint() string {
	switch {
	case a.search.active:
		query := a.search.query
		if query == "" {
			query = a.theme.tr("type to search this pane")
		}
		return fmt.Sprintf("/search: %s  ·  %d/%d  ·  %s", query, a.searchProgress(), len(a.search.hits), a.theme.tr("Enter keep · Esc cancel"))
	case a.search.nav:
		return fmt.Sprintf("%s  ·  %s", a.searchStatus(), a.theme.tr("n/N next · Esc done"))
	}
	return ""
}

func (a *App) searchProgress() int {
	if len(a.search.hits) == 0 {
		return 0
	}
	return a.search.current + 1
}

// handleSearchKey edits the query and commits it. Enter keeps the highlight and
// hands n/N to the main keymap; Esc cancels and restores the viewport.
func (a *App) handleSearchKey(ev *tcell.EventKey) {
	switch ev.Key() {
	case tcell.KeyEsc:
		a.closeSearch(true)
	case tcell.KeyEnter:
		if strings.TrimSpace(a.search.query) == "" {
			a.closeSearch(true)
			return
		}
		a.search.active = false
		a.search.nav = true
		a.setStatus(a.searchStatus(), false)
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if runes := []rune(a.search.query); len(runes) > 0 {
			a.search.query = string(runes[:len(runes)-1])
		}
		a.refreshSearch(true)
	case tcell.KeyCtrlU:
		a.search.query = ""
		a.refreshSearch(true)
	case tcell.KeyRune:
		if ev.Modifiers()&(tcell.ModCtrl|tcell.ModAlt) != 0 || ev.Str() == "" {
			return
		}
		a.search.query += ev.Str()
		a.refreshSearch(true)
	}
}
