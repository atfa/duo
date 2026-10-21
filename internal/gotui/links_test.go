package gotui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/atfa/duo/internal/workspace"
	"github.com/gdamore/tcell/v3"
)

func TestLinkifyFindsURLsAndExistingFiles(t *testing.T) {
	tm := newTheme()
	dir := t.TempDir()
	file := filepath.Join(dir, "note.md")
	if err := os.WriteFile(file, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runs := markdownRuns("see https://example.com/a, and `note.md`.", tm.Body, tm, 80)
	runs = linkifyRuns(runs, []string{dir}, tm)

	var url, path string
	for _, run := range runs {
		switch run.link.kind {
		case linkURL:
			url = run.link.value
		case linkFile:
			path = run.link.value
		}
	}
	if url != "https://example.com/a" {
		t.Fatalf("URL target = %q", url)
	}
	if path != file {
		t.Fatalf("file target = %q, want %q", path, file)
	}
}

func TestWrappedURLLinkSurvivesLineSplits(t *testing.T) {
	tm := newTheme()
	v := newTranscript(tm)
	v.SetRect(0, 0, 36, 12)
	target := "https://example.com/" + strings.Repeat("long-segment/", 8) + "tail"
	v.SetEntries([]entry{{pane: string(protocol.Austin), text: "see " + target}}, true)
	v.rebuild()

	for y := v.Inner.Min.Y; y < v.Inner.Max.Y; y++ {
		for x := v.Inner.Min.X; x < v.Inner.Max.X; x++ {
			link, ok := v.linkAt(x, y)
			if ok && link.kind == linkURL && link.value == target {
				return
			}
		}
	}
	t.Fatalf("wrapped URL target %q was not clickable", target)
}

func findLinkCell(t *testing.T, v *transcript, kind linkKind, value string) (int, int) {
	t.Helper()
	for y := v.Inner.Min.Y; y < v.Inner.Max.Y; y++ {
		for x := v.Inner.Min.X; x < v.Inner.Max.X; x++ {
			target, ok := v.linkAt(x, y)
			if ok && target.kind == kind && (value == "" || target.value == value) {
				return x, y
			}
		}
	}
	t.Fatalf("link %v %q was not found on screen", kind, value)
	return 0, 0
}

func TestMouseClickOpensURLAndFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "note.md")
	if err := os.WriteFile(file, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := newFakeSession()
	fake.view.Worktrees = workspace.Set{
		Repository: dir,
		Austin:     workspace.Worktree{Agent: protocol.Austin, Path: dir},
		Tony:       workspace.Worktree{Agent: protocol.Tony, Path: dir},
	}
	fake.history = []sessionstore.TUIEntry{
		{Time: time.Now(), Pane: "Duo", Text: "see https://example.com/path and note.md"},
	}
	app := New(fake)
	app.viewMode = viewSplit
	app.width, app.height = 120, 42
	app.layout = app.computeLayout(app.width, app.height)
	app.updateWidgets()

	var opened string
	app.openURL = func(target string) error { opened = "url:" + target; return nil }
	app.openFile = func(target string) error { opened = "file:" + target; return nil }

	x, y := findLinkCell(t, app.duo, linkURL, "https://example.com/path")
	app.handleMouse(tcell.NewEventMouse(x, y, tcell.Button1, tcell.ModNone))
	app.handleMouse(tcell.NewEventMouse(x, y, tcell.ButtonNone, tcell.ModNone))
	if opened != "url:https://example.com/path" {
		t.Fatalf("URL click opened %q", opened)
	}

	x, y = findLinkCell(t, app.duo, linkFile, file)
	app.handleMouse(tcell.NewEventMouse(x, y, tcell.Button1, tcell.ModNone))
	app.handleMouse(tcell.NewEventMouse(x, y, tcell.ButtonNone, tcell.ModNone))
	if opened != "file:"+file {
		t.Fatalf("file click opened %q, want %q", opened, "file:"+file)
	}
}

func TestOpenLinkUsesInjectedOpeners(t *testing.T) {
	var opened string
	app := &App{width: 120}
	app.openURL = func(target string) error {
		opened = "url:" + target
		return nil
	}
	app.openFile = func(target string) error {
		opened = "file:" + target
		return nil
	}
	app.openLink(linkTarget{kind: linkURL, value: "https://example.com/a"})
	if opened != "url:https://example.com/a" {
		t.Fatalf("URL opener = %q", opened)
	}

	file := filepath.Join(t.TempDir(), "note.md")
	if err := os.WriteFile(file, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	app.openLink(linkTarget{kind: linkFile, value: file})
	if opened != "file:"+file {
		t.Fatalf("file opener = %q", opened)
	}
}
