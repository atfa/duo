package gotui

import (
	"strings"
	"testing"

	rw "github.com/mattn/go-runewidth"
)

// helpText renders the /help sections as plain text, the same strings the
// overlay translates and wraps.
func helpText() string {
	var b strings.Builder
	for _, section := range helpSections() {
		b.WriteString(section.title)
		b.WriteByte('\n')
		for _, line := range section.lines {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// TestHelpListsEverySlashCommand keeps the guide in step with the prompt: every
// command the composer accepts must be named in /help, including aliases.
func TestHelpListsEverySlashCommand(t *testing.T) {
	text := helpText()
	for _, command := range slashCommands {
		if !strings.Contains(text, command.name) {
			t.Errorf("/help does not list %s", command.name)
		}
	}
	if !strings.Contains(text, "/exit") {
		t.Error("/help does not document the /exit alias")
	}
	for _, mode := range []string{"FAST", "GOAL", "STEP"} {
		if !strings.Contains(text, mode) {
			t.Errorf("/help does not explain %s mode", mode)
		}
	}
}

// TestHelpHasChineseTranslations pins the other half of the guide: every heading
// and hand-written line needs a zh translation, otherwise /language zh leaves
// English text behind. Generated command rows in SLASH COMMANDS are canonical
// tokens and are skipped; the hand-written /pause and /driver guidance stays
// covered.
func TestHelpHasChineseTranslations(t *testing.T) {
	for _, section := range helpSections() {
		if zhUI[section.title] == "" {
			t.Errorf("no zh translation for help section %q", section.title)
		}
		for _, line := range section.lines {
			if section.title == "SLASH COMMANDS" && strings.HasPrefix(line, "/") {
				continue
			}
			if zhUI[line] == "" {
				t.Errorf("no zh translation for help line %q", line)
			}
		}
	}
}

func TestObserverShortcutIsAlwaysVisibleInFooter(t *testing.T) {
	app := New(newFakeSession())
	for _, lang := range []string{"en", "zh"} {
		*app.theme.Lang = lang
		for _, width := range []int{120, 117, 110, 105, 100, 90, 70} {
			app.layout.width = width
			hint := app.footerHint()
			shortcut := "Ctrl+E observer"
			if lang == "zh" {
				shortcut = "Ctrl+E 观察器"
			}
			if !strings.Contains(hint, shortcut) {
				t.Errorf("footer at width %d, language %s = %q, want %q hint", width, lang, hint, shortcut)
			}
			if width >= 100 && rw.StringWidth(hint) > width-2 {
				t.Errorf("footer hint at width %d, language %s is %d columns and will be truncated: %q", width, lang, rw.StringWidth(hint), hint)
			}
		}
	}
}

// TestSlashDescriptionsHaveChineseTranslations covers the palette: every
// command description is rendered through zhUI, so a missing entry leaves
// English text in the /language zh palette.
func TestSlashDescriptionsHaveChineseTranslations(t *testing.T) {
	for _, command := range slashCommands {
		if command.desc == "" {
			continue
		}
		if zhUI[command.desc] == "" {
			t.Errorf("no zh translation for %s description %q", command.name, command.desc)
		}
	}
}

// TestChunkJoinWrapsCommandList pins the packing used by the generated slash
// section.
func TestChunkJoinWrapsCommandList(t *testing.T) {
	got := chunkJoin([]string{"/a", "/b", "/c", "/d"}, 3)
	want := []string{"/a · /b · /c", "/d"}
	if len(got) != len(want) {
		t.Fatalf("chunkJoin = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("chunkJoin = %#v, want %#v", got, want)
		}
	}
}
