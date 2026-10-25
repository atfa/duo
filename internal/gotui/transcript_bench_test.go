package gotui

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// transcriptEntry builds one synthetic message. The body is prose with the
// density of a real report, so rendering it exercises markdown, linkify and
// wrapping rather than a single word.
func transcriptEntry(seq int, text string) entry {
	return entry{
		at:    time.Date(2026, 10, 20, 16, 2, 0, 0, time.Local).Add(time.Duration(seq) * time.Second),
		pane:  "Austin",
		label: "Austin → Human",
		text:  text,
		seq:   seq,
	}
}

func transcriptBody(bytes int) string {
	const sentence = "Implemented the cursor pagination and documented the new parameters in the API reference. "
	var b strings.Builder
	for b.Len() < bytes {
		b.WriteString(sentence)
	}
	return b.String()[:bytes]
}

// BenchmarkTranscriptAppend measures one message arriving in a full window:
// 400 entries of 1KB each, 100 columns by 40 rows, timestamps on, no path roots,
// each iteration appending the next entry and dropping the oldest (the App's
// 400-entry window). Every iteration carries a new seq, so SetEntries really
// rebuilds instead of taking its unchanged-list short circuit.
func BenchmarkTranscriptAppend(b *testing.B) {
	v := newTranscript(newTheme())
	v.SetRect(0, 0, 100, 40)
	body := transcriptBody(1024)
	// The backing slice only grows: passing a fresh window sub-slice each round
	// is what the App does, and an in-place shift would hide the append from
	// SetEntries' seq comparison.
	all := make([]entry, 0, 401+b.N)
	for i := 1; i <= 400; i++ {
		all = append(all, transcriptEntry(i, body))
	}
	v.SetEntries(all, true)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		all = append(all, transcriptEntry(401+i, body))
		v.SetEntries(all[len(all)-400:], true)
	}
}

func renderLineTexts(lines []renderLine) string {
	var b strings.Builder
	for _, line := range lines {
		fmt.Fprintf(&b, "%d:%s\n", line.entry, line.text())
	}
	return b.String()
}

// TestTranscriptIncrementalMatchesFullRebuild is the guard against a cache that
// renders differently from a rebuild: appending and evicting must leave exactly
// the lines a transcript built from the same window would have.
func TestTranscriptIncrementalMatchesFullRebuild(t *testing.T) {
	tm := newTheme()
	v := newTranscript(tm)
	v.SetRect(0, 0, 60, 20)
	all := make([]entry, 0, 40)
	for i := 1; i <= 30; i++ {
		all = append(all, transcriptEntry(i, transcriptBody(120)+fmt.Sprintf(" entry %d", i)))
	}
	v.SetEntries(all, true)
	for i := 31; i <= 40; i++ {
		all = append(all, transcriptEntry(i, transcriptBody(120)))
		v.SetEntries(all[len(all)-20:], true)
	}

	fresh := newTranscript(tm)
	fresh.SetRect(0, 0, 60, 20)
	fresh.SetEntries(all[len(all)-20:], true)
	if got, want := renderLineTexts(v.lines), renderLineTexts(fresh.lines); got != want {
		t.Fatalf("incremental lines differ from a full rebuild:\n got %q\nwant %q", got, want)
	}
	if len(v.blocks) != 20 {
		t.Fatalf("cache holds %d blocks, want the 20 entries in the window", len(v.blocks))
	}
}

// TestTranscriptCacheConvergesToTheWindow proves the cache is bounded: the App
// caps its entries at 400, and the evicted entries must take their rendered
// lines with them instead of leaving the cache to grow with the session.
func TestTranscriptCacheConvergesToTheWindow(t *testing.T) {
	v := newTranscript(newTheme())
	v.SetRect(0, 0, 80, 20)
	all := make([]entry, 0, 600)
	for i := 1; i <= 600; i++ {
		all = append(all, transcriptEntry(i, "message"))
		v.SetEntries(all[max(0, len(all)-100):], true)
	}
	if len(v.blocks) != 100 {
		t.Fatalf("cache holds %d blocks, want the 100 entries in the window", len(v.blocks))
	}
	if len(v.lines) != blockLineCount(v.blocks) {
		t.Fatalf("flat lines = %d, blocks account for %d", len(v.lines), blockLineCount(v.blocks))
	}
	// The flat list is compacted in place, so its backing array must stay close
	// to the window too; otherwise memory still grows with the session.
	if cap(v.lines) > 2*len(v.lines) {
		t.Fatalf("flat line capacity = %d for %d lines, want the window's size", cap(v.lines), len(v.lines))
	}
}

// TestTranscriptCacheInvalidatesOnWidthChange checks the width is part of the
// cache key: a narrower pane must re-wrap the cached entries, not draw stale rows.
func TestTranscriptCacheInvalidatesOnWidthChange(t *testing.T) {
	v := newTranscript(newTheme())
	v.SetRect(0, 0, 80, 20)
	entries := []entry{transcriptEntry(1, strings.Repeat("word ", 60))}
	v.SetEntries(entries, true)
	wide := len(v.lines)
	v.SetRect(0, 0, 30, 20)
	v.SetEntries(entries, true)
	if v.renderWidth != wrapWidth(v.Inner.Dx()) {
		t.Fatalf("render width = %d, want %d", v.renderWidth, wrapWidth(v.Inner.Dx()))
	}
	if len(v.lines) <= wide {
		t.Fatalf("narrowing produced %d lines, want more than the %d wide lines", len(v.lines), wide)
	}
}

// TestTranscriptCacheInvalidatesOnLanguageChange checks the interface language is
// part of the cache key: the summary header is translated, so a /language switch
// must not leave the previous wording in cached lines.
func TestTranscriptCacheInvalidatesOnLanguageChange(t *testing.T) {
	tm := newTheme()
	v := newTranscript(tm)
	v.SetRect(0, 0, 80, 20)
	entries := []entry{{at: time.Now(), pane: "Austin", label: "Austin → Human", text: "done", summary: true, seq: 1}}
	v.SetEntries(entries, true)
	if !strings.Contains(renderLineTexts(v.lines), "FINAL REPORT") {
		t.Fatalf("summary header was not rendered in English: %q", renderLineTexts(v.lines))
	}
	*tm.Lang = "zh"
	v.SetEntries(entries, true)
	if !strings.Contains(renderLineTexts(v.lines), "最终报告") {
		t.Fatalf("summary header was not re-rendered in Chinese: %q", renderLineTexts(v.lines))
	}
}

// TestTranscriptCacheSkipsUnkeyedEntries covers the boundary the cache must not
// cross: entries without a usable seq (a hand-built test window) are rendered
// fresh rather than reused for one another.
func TestTranscriptCacheSkipsUnkeyedEntries(t *testing.T) {
	v := newTranscript(newTheme())
	v.SetRect(0, 0, 80, 20)
	v.SetEntries([]entry{{pane: "Austin", text: "first"}, {pane: "Austin", text: "second"}}, true)
	v.SetEntries([]entry{{pane: "Austin", text: "third"}}, true)
	if got := renderLineTexts(v.lines); !strings.Contains(got, "third") || strings.Contains(got, "first") {
		t.Fatalf("unkeyed entry reused a cached block: %q", got)
	}
}
