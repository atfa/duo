package gotui

import (
	"strings"
	"testing"
	"time"
)

// The direction label is stored in its canonical English form (and restored that
// way from the session log), so the zh dashboard has to translate it while it
// renders. Only the human side is chrome: agent names stay canonical, exactly
// like the panel titles.
func TestTranscriptTranslatesDirectionLabels(t *testing.T) {
	for _, tc := range []struct {
		lang, label, want string
	}{
		{"en", "Human → Austin", "Human → Austin"},
		{"zh", "Human → Austin", "人类 → Austin"},
		{"zh", "Duo → Human", "Duo → 人类"},
		{"zh", "Austin → Tony", "Austin → Tony"},
		{"zh", "Duo", "Duo"},
		{"zh", "notice without a direction", "notice without a direction"},
	} {
		th := newTheme()
		*th.Lang = tc.lang
		v := newTranscript(th)
		v.conversation = true
		block := v.renderEntry(entry{label: tc.label, pane: "Duo", text: "hello", seq: 1}, 60)
		if len(block.lines) == 0 {
			t.Fatalf("%s/%q: no lines rendered", tc.lang, tc.label)
		}
		header := block.lines[0].text()
		if !strings.Contains(header, tc.want) {
			t.Fatalf("%s: header = %q, want it to contain %q", tc.lang, header, tc.want)
		}
	}
}

// A language switch must re-render the stored labels, not only new entries: the
// cached blocks are keyed on renderLang.
func TestTranscriptRerendersLabelsAfterLanguageSwitch(t *testing.T) {
	th := newTheme()
	v := newTranscript(th)
	v.SetRect(0, 0, 60, 8)
	v.SetEntries([]entry{{seq: 1, at: time.Now(), pane: "Duo", label: "Human → Austin", text: "hello"}}, true)
	if text := v.lines[0].text(); !strings.Contains(text, "Human → Austin") {
		t.Fatalf("en header = %q", text)
	}
	*th.Lang = "zh"
	v.SetEntries([]entry{{seq: 1, at: time.Now(), pane: "Duo", label: "Human → Austin", text: "hello"}}, true)
	if text := v.lines[0].text(); !strings.Contains(text, "人类 → Austin") {
		t.Fatalf("zh header after switch = %q", text)
	}
}
