package gotui

import (
	"image"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/session"
	"github.com/gdamore/tcell/v3"
	ui "github.com/metaspartan/gotui/v5"
)

func pressText(t *testing.T, app *App, text string) {
	t.Helper()
	for _, r := range text {
		app.handleKey(tcell.NewEventKey(tcell.KeyRune, string(r), tcell.ModNone))
	}
}

func mentionApp(t *testing.T, files ...string) (*App, *int) {
	t.Helper()
	fake := newFakeSession()
	app := New(fake)
	calls := 0
	app.listFiles = func(repo string) []string {
		calls++
		if repo != fake.view.Worktrees.Repository {
			t.Fatalf("listFiles repository = %q, want %q", repo, fake.view.Worktrees.Repository)
		}
		return files
	}
	return app, &calls
}

func TestFilterMentionFilesRanksBasenamePrefix(t *testing.T) {
	files := []string{
		"README.md",
		"internal/gotui/keys.go",
		"docs/keys.md",
		"internal/keys/other.go",
		"internal/gotui/composer.go",
	}
	got := filterMentionFiles(files, "KEY")
	want := []string{"internal/gotui/keys.go", "docs/keys.md", "internal/keys/other.go"}
	if len(got) != len(want) {
		t.Fatalf("matches = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("matches = %q, want %q", got, want)
		}
	}
}

func TestFilterMentionFilesCapsLargeLists(t *testing.T) {
	files := make([]string, mentionLimit+20)
	for i := range files {
		files[i] = "dir/file.go"
	}
	if got := filterMentionFiles(files, ""); len(got) != mentionLimit {
		t.Fatalf("unfiltered matches = %d, want %d", len(got), mentionLimit)
	}
	if got := filterMentionFiles(files, "file"); len(got) != mentionLimit {
		t.Fatalf("filtered matches = %d, want %d", len(got), mentionLimit)
	}
}

func TestMentionPathFromScope(t *testing.T) {
	cases := []struct {
		scope, file, want string
	}{
		{"", "internal/gotui/keys.go", "internal/gotui/keys.go"},
		{".", "internal/gotui/keys.go", "internal/gotui/keys.go"},
		{"internal/gotui", "internal/gotui/keys.go", "keys.go"},
		{"internal/gotui", "internal/keys/other.go", "../keys/other.go"},
		{"internal/gotui", "README.md", "../../README.md"},
	}
	for _, tc := range cases {
		if got := mentionPathFromScope(tc.scope, tc.file); got != tc.want {
			t.Fatalf("mentionPathFromScope(%q, %q) = %q, want %q", tc.scope, tc.file, got, tc.want)
		}
	}
}

func TestFormatMentionQuotesPathsWithSpaces(t *testing.T) {
	if got := formatMention("internal/gotui/keys.go"); got != "@internal/gotui/keys.go " {
		t.Fatalf("formatMention = %q", got)
	}
	if got := formatMention("docs/a b.md"); got != "@`docs/a b.md` " {
		t.Fatalf("formatMention with spaces = %q", got)
	}
}

func TestComposerReplaceRangeReplacesOneLineToken(t *testing.T) {
	c := newComposer(newTheme())
	c.SetText("look at @key\nnext")
	c.row, c.col = 0, len("look at @key")
	if !c.ReplaceRange(0, len("look at "), c.col, "@keys.go ") {
		t.Fatal("ReplaceRange refused a valid token")
	}
	if got, want := c.Text(), "look at @keys.go \nnext"; got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
	if c.row != 0 || c.col != len("look at @keys.go ") {
		t.Fatalf("cursor = %d,%d, want 0,%d", c.row, c.col, len("look at @keys.go "))
	}
	if c.ReplaceRange(0, 0, 1, "a\nb") {
		t.Fatal("ReplaceRange accepted a newline")
	}
	if c.ReplaceRange(0, 0, len(c.lines[0])+1, "x") {
		t.Fatal("ReplaceRange accepted an out-of-range span")
	}
}

func TestMentionPaletteFiltersAcceptsAndSubmits(t *testing.T) {
	app, calls := mentionApp(t,
		"README.md",
		"docs/gotui.md",
		"internal/gotui/composer.go",
		"internal/gotui/keys.go",
	)

	pressText(t, app, "read ")
	app.handleKey(tcell.NewEventKey(tcell.KeyRune, "@", tcell.ModNone))
	if !app.mentionOpen() {
		t.Fatal("@ at a word start did not open the file palette")
	}
	if len(app.fileMenu.matches) != 4 {
		t.Fatalf("initial matches = %q, want all four files", app.fileMenu.matches)
	}
	pressText(t, app, "key")
	if got := app.fileMenu.matches; len(got) != 1 || got[0] != "internal/gotui/keys.go" {
		t.Fatalf("matches after filtering = %q, want only keys.go", got)
	}
	if *calls != 1 {
		t.Fatalf("listFiles calls while typing = %d, want one snapshot per open", *calls)
	}

	app.handleKey(tcell.NewEventKey(tcell.KeyEnter, "", tcell.ModNone))
	if got, want := app.composer.Text(), "read @internal/gotui/keys.go "; got != want {
		t.Fatalf("composer after accepting = %q, want %q", got, want)
	}
	if app.mentionOpen() {
		t.Fatal("the palette stayed open after accepting a file")
	}

	// Enter now means submit, not accept.
	app.handleKey(tcell.NewEventKey(tcell.KeyEnter, "", tcell.ModNone))
	commands := app.svc.(*fakeSession).snapshotCommands()
	if len(commands) != 1 {
		t.Fatalf("commands after submit = %d, want 1", len(commands))
	}
	submit, ok := commands[0].(session.SubmitTask)
	if !ok || submit.Text != "read @internal/gotui/keys.go" {
		t.Fatalf("submitted command = %#v", commands[0])
	}

	// A new palette gets a fresh snapshot: landing a step can change the file list.
	pressText(t, app, "@")
	if *calls != 2 {
		t.Fatalf("listFiles calls after reopening = %d, want 2", *calls)
	}
}

func TestMentionPaletteUsesPathsFromTheAgentScope(t *testing.T) {
	fake := newFakeSession()
	fake.view.Worktrees.ScopePath = "internal/gotui"
	app := New(fake)
	app.listFiles = func(string) []string {
		return []string{"internal/gotui/keys.go", "README.md"}
	}
	app.handleKey(tcell.NewEventKey(tcell.KeyRune, "@", tcell.ModNone))
	if got := app.fileMenu.matches; len(got) != 2 || got[0] != "keys.go" || got[1] != "../../README.md" {
		t.Fatalf("scoped matches = %q, want paths relative to the agent's cwd", got)
	}
}

func TestMentionPaletteReplacesTheWholeTokenFromTheMiddle(t *testing.T) {
	app, _ := mentionApp(t, "internal/gotui/keys.go")
	pressText(t, app, "see @keys")
	app.handleKey(tcell.NewEventKey(tcell.KeyLeft, "", tcell.ModNone))
	app.handleKey(tcell.NewEventKey(tcell.KeyLeft, "", tcell.ModNone))
	if !app.mentionOpen() {
		t.Fatal("the palette closed with the cursor inside the token")
	}
	app.handleKey(tcell.NewEventKey(tcell.KeyEnter, "", tcell.ModNone))
	if got, want := app.composer.Text(), "see @internal/gotui/keys.go "; got != want {
		t.Fatalf("composer after mid-token accept = %q, want %q", got, want)
	}
}

func TestMentionPaletteNavigationAndTab(t *testing.T) {
	app, _ := mentionApp(t, "a.md", "b.md", "c.md")
	app.handleKey(tcell.NewEventKey(tcell.KeyRune, "@", tcell.ModNone))
	if !app.mentionOpen() {
		t.Fatal("@ did not open the file palette")
	}
	app.handleKey(tcell.NewEventKey(tcell.KeyDown, "", tcell.ModNone))
	app.handleKey(tcell.NewEventKey(tcell.KeyTab, "", tcell.ModNone))
	if got, want := app.composer.Text(), "@b.md "; got != want {
		t.Fatalf("composer after Tab = %q, want %q", got, want)
	}
}

func TestMentionPaletteRequiresAWordBoundary(t *testing.T) {
	app, calls := mentionApp(t, "keys.go")
	pressText(t, app, "mail a@b")
	if app.mentionOpen() {
		t.Fatal("an @ inside a word opened the file palette")
	}
	if *calls != 0 {
		t.Fatalf("listFiles calls = %d, want none for an email", *calls)
	}

	// Punctuation is a word boundary, so a mention after "(" still works.
	pressText(t, app, " (@")
	if !app.mentionOpen() {
		t.Fatal("@ after punctuation did not open the file palette")
	}

	// Chinese is normally typed without a space before the mention, so a
	// non-ASCII letter cannot block the trigger.
	cjk, cjkCalls := mentionApp(t, "keys.go")
	pressText(t, cjk, "看看@")
	if !cjk.mentionOpen() {
		t.Fatal("@ after Chinese text did not open the file palette")
	}
	if *cjkCalls != 1 {
		t.Fatalf("listFiles calls after Chinese text = %d, want 1", *cjkCalls)
	}
}

func TestMentionPaletteIgnoresBracketedPaste(t *testing.T) {
	app, calls := mentionApp(t, "keys.go")
	app.handleEvent(tcell.NewEventPaste(true))
	pressText(t, app, "@keys")
	app.handleEvent(tcell.NewEventPaste(false))
	if app.mentionOpen() {
		t.Fatal("pasted @ text opened the file palette")
	}
	if *calls != 0 {
		t.Fatalf("listFiles calls = %d, want none for a paste", *calls)
	}
	if got, want := app.composer.Text(), "@keys"; got != want {
		t.Fatalf("composer after paste = %q, want %q", got, want)
	}
}

func TestMentionPaletteEscAndSpaceDismiss(t *testing.T) {
	app, _ := mentionApp(t, "keys.go")
	pressText(t, app, "@")
	if !app.mentionOpen() {
		t.Fatal("@ did not open the file palette")
	}
	app.handleKey(tcell.NewEventKey(tcell.KeyEsc, "", tcell.ModNone))
	if app.mentionOpen() {
		t.Fatal("Esc did not dismiss the file palette")
	}

	app.handleKey(tcell.NewEventKey(tcell.KeyRune, "@", tcell.ModNone))
	if !app.mentionOpen() {
		t.Fatal("the palette did not reopen")
	}
	pressText(t, app, " ")
	if app.mentionOpen() {
		t.Fatal("a space after the query did not dismiss the file palette")
	}
}

func TestMentionPaletteSurvivesAnEmptyQuery(t *testing.T) {
	app, _ := mentionApp(t, "abc.py")
	pressText(t, app, "@acc")
	if !app.mentionActive() {
		t.Fatal("a query with no match closed the palette")
	}
	if app.mentionOpen() {
		t.Fatal("an empty palette accepted Enter/Tab")
	}
	if got := app.fileMenu.matches; len(got) != 0 {
		t.Fatalf("matches = %q, want none", got)
	}

	// Repairing the typo brings the file back without retyping the @.
	app.handleKey(tcell.NewEventKey(tcell.KeyBackspace2, "", tcell.ModNone))
	app.handleKey(tcell.NewEventKey(tcell.KeyBackspace2, "", tcell.ModNone))
	if !app.mentionOpen() {
		t.Fatal("backspacing to a matching query did not restore the results")
	}
	app.handleKey(tcell.NewEventKey(tcell.KeyEnter, "", tcell.ModNone))
	if got, want := app.composer.Text(), "@abc.py "; got != want {
		t.Fatalf("composer after accepting = %q, want %q", got, want)
	}
}

func TestMentionPaletteEscDismissesAnEmptyQuery(t *testing.T) {
	app, calls := mentionApp(t, "abc.py")
	pressText(t, app, "@acc")
	app.handleKey(tcell.NewEventKey(tcell.KeyEsc, "", tcell.ModNone))
	if app.mentionActive() {
		t.Fatal("Esc did not dismiss the empty palette")
	}
	// The dismissal sticks: editing the token does not reopen the palette, and
	// the file list is not listed again.
	pressText(t, app, "a")
	if app.mentionActive() {
		t.Fatal("the dismissed palette reopened while editing its token")
	}
	if *calls != 1 {
		t.Fatalf("listFiles calls = %d, want one snapshot per open", *calls)
	}
}

func TestMentionMenuKeepsContentInATightBox(t *testing.T) {
	menu := func(matches []string) *mentionMenu {
		m := newMentionMenu(newTheme())
		m.SetRect(0, 0, 20, 3)
		m.matches = matches
		return m
	}
	// A box clamped to three rows has a single inner row. The hint used to be
	// drawn over it, hiding the file name or the no-match notice.
	innerRow := func(m *mentionMenu) string {
		buf := ui.NewBuffer(m.GetRect())
		m.Draw(buf)
		var b strings.Builder
		for x := m.Inner.Min.X; x < m.Inner.Max.X; x++ {
			b.WriteRune(buf.GetCell(image.Pt(x, m.Inner.Min.Y)).Rune)
		}
		return b.String()
	}
	if got := innerRow(menu([]string{"abc.py"})); !strings.Contains(got, "abc.py") {
		t.Fatalf("tight box row = %q, want the file name", got)
	}
	if got := innerRow(menu(nil)); !strings.Contains(got, "no files found") {
		t.Fatalf("empty tight box row = %q, want the notice", got)
	}
}
