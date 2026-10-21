package gotui

import (
	"image"
	"os/exec"
	"path"
	"strings"
	"unicode"

	ui "github.com/metaspartan/gotui/v5"
)

// mentionLimit bounds the work a filter does on a large repository. The panel
// only shows a handful of rows; keeping the rest lets the user narrow instead of
// scanning thousands of paths.
const mentionLimit = 100

// gitListFiles returns the repository's tracked files, relative to its root.
// Only tracked files are listed: agents read their own worktree, where a file
// that the human has not committed does not exist.
func gitListFiles(repo string) []string {
	if strings.TrimSpace(repo) == "" {
		return nil
	}
	out, err := exec.Command("git", "-C", repo, "ls-files", "-z").Output()
	if err != nil {
		return nil
	}
	fields := strings.Split(string(out), "\x00")
	files := make([]string, 0, len(fields))
	for _, file := range fields {
		if file == "" || strings.ContainsAny(file, "\n\r") {
			continue
		}
		files = append(files, file)
	}
	return files
}

// mentionBoundary reports whether a rune typed at col starts a word, which is
// where an @ opens the file palette. Only ASCII word characters block it: an
// email's @ has a Latin letter before it, while Chinese text is normally typed
// without a space before the mention ("看看@key"). A non-ASCII address such as
// josé@example.com therefore opens the palette, which Esc dismisses.
func mentionBoundary(line []rune, col int) bool {
	if col <= 0 {
		return true
	}
	prev := line[col-1]
	if prev >= 128 {
		return true
	}
	return !(prev >= 'a' && prev <= 'z' ||
		prev >= 'A' && prev <= 'Z' ||
		prev >= '0' && prev <= '9' ||
		prev == '_')
}

// mentionTokenEnd returns the end of the @ token that starts at start: the next
// whitespace, or the end of the line. Accepting replaces the whole token even
// when the cursor sits inside it.
func mentionTokenEnd(line []rune, start int) int {
	for i := start + 1; i < len(line); i++ {
		if unicode.IsSpace(line[i]) {
			return i
		}
	}
	return len(line)
}

// filterMentionFiles ranks a basename prefix above a substring match, so
// `@keys` finds internal/gotui/keys.go before a path that merely contains
// "keys". The original Git order is preserved inside each group.
func filterMentionFiles(files []string, query string) []string {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		if len(files) > mentionLimit {
			return files[:mentionLimit]
		}
		return files
	}
	prefix := make([]string, 0, len(files))
	substring := make([]string, 0, len(files))
	for _, file := range files {
		lower := strings.ToLower(file)
		if strings.HasPrefix(strings.ToLower(path.Base(file)), query) {
			prefix = append(prefix, file)
			continue
		}
		if strings.Contains(lower, query) {
			substring = append(substring, file)
		}
	}
	matches := append(prefix, substring...)
	if len(matches) > mentionLimit {
		matches = matches[:mentionLimit]
	}
	return matches
}

// mentionPathFromScope maps a repository-relative Git path into the path an
// agent sees from its scoped working directory. Without this, a session started
// in a subdirectory would insert a repository-root path that the agent resolves
// from the wrong directory.
func mentionPathFromScope(scope, file string) string {
	scope = path.Clean(strings.TrimSpace(scope))
	if scope == "" || scope == "." {
		return file
	}
	from := strings.Split(scope, "/")
	to := strings.Split(file, "/")
	common := 0
	for common < len(from) && common < len(to)-1 && from[common] == to[common] {
		common++
	}
	parts := make([]string, 0, len(from)-common+len(to)-common)
	for i := common; i < len(from); i++ {
		parts = append(parts, "..")
	}
	parts = append(parts, to[common:]...)
	return strings.Join(parts, "/")
}

// formatMention renders the text inserted for a file. A path with spaces is
// wrapped in backticks so it stays one readable token in the agent prompt.
func formatMention(file string) string {
	if strings.ContainsAny(file, " \t") {
		return "@`" + file + "` "
	}
	return "@" + file + " "
}

// mentionMenu is the floating file list shown above the composer while an @
// token is being completed.
type mentionMenu struct {
	*ui.Block
	theme   theme
	matches []string
	cursor  int
	offset  int
	box     image.Rectangle
}

func newMentionMenu(t theme) *mentionMenu {
	m := &mentionMenu{Block: newModalBlock(t), theme: t}
	m.Title = " FILES "
	return m
}

func (m *mentionMenu) place(l layout, c *composer) {
	r := l.main
	if r.Dx() < 10 || r.Dy() < 3 {
		r = image.Rect(1, max(0, c.Min.Y-3), max(11, l.width-1), c.Min.Y)
	}
	height := min(10, len(m.matches)+3)
	if height < 3 {
		height = 3
	}
	if available := c.Min.Y - r.Min.Y; height > available {
		height = available
	}
	if height < 3 {
		height = 3
	}
	x, width := r.Min.X, r.Dx()
	y := max(r.Min.Y, c.Min.Y-height)
	m.box = image.Rect(x, y, x+width, y+height)
	m.SetRect(m.box.Min.X, m.box.Min.Y, m.box.Max.X, m.box.Max.Y)
}

func (m *mentionMenu) clamp() {
	if m.cursor >= len(m.matches) {
		m.cursor = len(m.matches) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m *mentionMenu) move(delta int) {
	if len(m.matches) == 0 {
		return
	}
	m.cursor = (m.cursor + delta + len(m.matches)) % len(m.matches)
}

func (m *mentionMenu) selected() (string, bool) {
	if len(m.matches) == 0 {
		return "", false
	}
	m.clamp()
	return m.matches[m.cursor], true
}

// mentionOpen reports whether the file palette is showing. The palette owns
// Enter and Tab only while it has a selectable result.
func (a *App) mentionOpen() bool {
	return a.mentionStart >= 0 && a.fileMenu != nil && len(a.fileMenu.matches) > 0
}

// openMention snapshots the tracked file list for one palette session. The list
// is refreshed on every open rather than cached for the session: landing a step
// moves the user's branch, and a session-long list would then offer paths that
// no longer exist.
func (a *App) openMention(start int) {
	a.closeMention()
	if a.listFiles == nil {
		return
	}
	repo := a.live.Worktrees.Repository
	if repo == "" {
		repo = a.live.Worktrees.Root
	}
	files := a.listFiles(repo)
	if len(files) == 0 {
		return
	}
	scope := a.live.Worktrees.ScopePath
	scoped := make([]string, 0, len(files))
	for _, file := range files {
		scoped = append(scoped, mentionPathFromScope(scope, file))
	}
	files = scoped
	matches := filterMentionFiles(files, "")
	if len(matches) == 0 {
		return
	}
	row, _ := a.composer.Cursor()
	a.mentionStart = start
	a.mentionRow = row
	a.mentionQuery = ""
	a.mentionFiles = files
	a.fileMenu.matches = matches
	a.fileMenu.cursor = 0
	a.fileMenu.offset = 0
}

// syncMention keeps the palette tied to the @ token under the cursor. It runs
// after every key, so editing the token filters, and moving outside it closes.
func (a *App) syncMention() {
	if a.mentionStart < 0 {
		return
	}
	row, col := a.composer.Cursor()
	line := a.composer.CurrentLine()
	if row != a.mentionRow || a.mentionStart >= len(line) || a.mentionStart >= col || line[a.mentionStart] != '@' {
		a.closeMention()
		return
	}
	if !mentionBoundary(line, a.mentionStart) {
		a.closeMention()
		return
	}
	query := string(line[a.mentionStart+1 : col])
	if strings.ContainsAny(query, " \t\n\r") {
		a.closeMention()
		return
	}
	matches := filterMentionFiles(a.mentionFiles, query)
	if len(matches) == 0 {
		a.closeMention()
		return
	}
	if query != a.mentionQuery {
		a.mentionQuery = query
		a.fileMenu.cursor = 0
		a.fileMenu.offset = 0
	}
	a.fileMenu.matches = matches
	a.fileMenu.clamp()
}

func (a *App) closeMention() {
	a.mentionStart = -1
	a.mentionRow = 0
	a.mentionQuery = ""
	a.mentionFiles = nil
	if a.fileMenu != nil {
		a.fileMenu.matches = nil
		a.fileMenu.cursor = 0
		a.fileMenu.offset = 0
	}
}

func (a *App) acceptMention() {
	file, ok := a.fileMenu.selected()
	if !ok {
		a.closeMention()
		return
	}
	row, col := a.composer.Cursor()
	line := a.composer.CurrentLine()
	if row != a.mentionRow || col <= a.mentionStart || a.mentionStart >= len(line) || line[a.mentionStart] != '@' {
		a.closeMention()
		return
	}
	end := mentionTokenEnd(line, a.mentionStart)
	if !a.composer.ReplaceRange(row, a.mentionStart, end, formatMention(file)) {
		a.closeMention()
		return
	}
	a.closeMention()
	a.resetHistory()
}

func (m *mentionMenu) Draw(buf *ui.Buffer) {
	fillBlock(buf, m.GetRect(), ui.Style{Fg: m.theme.Fg, Bg: m.theme.PanelHigh})
	m.Title = " " + m.theme.tr("FILES") + " "
	m.Block.Draw(buf)
	m.clamp()
	rows := max(1, m.Inner.Dy()-1)
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+rows {
		m.offset = m.cursor - rows + 1
	}
	maxOffset := max(0, len(m.matches)-rows)
	if m.offset > maxOffset {
		m.offset = maxOffset
	}
	for i := 0; i < rows; i++ {
		idx := m.offset + i
		if idx >= len(m.matches) {
			break
		}
		style := m.theme.Body
		y := m.Inner.Min.Y + i
		if idx == m.cursor {
			style = ui.Style{Fg: m.theme.Bg, Bg: m.theme.Accent, Modifier: ui.ModifierBold}
			fillBlock(buf, image.Rect(m.Inner.Min.X, y, m.Inner.Max.X, y+1), style)
		}
		buf.SetString(fitText(m.matches[idx], m.Inner.Dx()), style, image.Pt(m.Inner.Min.X, y))
	}
	if m.Inner.Dy() > 0 {
		buf.SetString(fitText(m.theme.tr("↑/↓ choose  ·  Tab/Enter insert  ·  Esc dismiss"), m.Inner.Dx()), m.theme.Timestamp, image.Pt(m.Inner.Min.X, m.Inner.Max.Y-1))
	}
}
