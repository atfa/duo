package sessionstore

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// LogsDirName is the directory under the repository root that holds the
// human-readable session transcripts. It sits beside config.json, inside the
// .duo/ directory that EnsureGitIgnore already hides via .git/info/exclude, so
// writing a transcript never dirties the repository.
const LogsDirName = "logs"

// LogWriter appends the visible session transcript to Markdown files in the
// main repository, so a finished session can be read without opening the
// session journal or hunting for a worktree.
//
// It mirrors the journal rather than replacing it: events.jsonl stays the
// machine-readable record that a resume restores from, and these files are the
// copy a person reads. Entries are appended as they are displayed, so a session
// that is interrupted still leaves a transcript behind.
//
// A LogWriter is safe for concurrent use and never returns an error from
// Append: a transcript must not be able to break the session it describes.
type LogWriter struct {
	mu      sync.Mutex
	full    *os.File
	agents  map[string]*os.File
	started time.Time
}

// Transcript describes the session a set of transcripts belongs to. It is
// written into each file's header.
type Transcript struct {
	// SessionID names the files. Duo session ids already begin with a sortable
	// timestamp, so the filename is both a timestamp and unique.
	SessionID string
	Mode      string
	// Repository is the main repository root, never a worktree: the point of
	// these files is that they are where the user works, not inside a throwaway
	// checkout of an agent branch.
	Repository string
	Branch     string
	BaseCommit string
}

// NewLogWriter creates the transcript files for a session, replacing any
// transcripts left by a previous run of the same session id. That is what makes
// a resume idempotent: the resumed run rewrites the file from its own start
// rather than appending to a half-written one.
func NewLogWriter(repoRoot string, info Transcript) (*LogWriter, error) {
	repoRoot = strings.TrimSpace(repoRoot)
	if repoRoot == "" || strings.TrimSpace(info.SessionID) == "" {
		return nil, nil
	}
	dir := filepath.Join(repoRoot, ".duo", LogsDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	w := &LogWriter{agents: map[string]*os.File{}, started: time.Now()}

	full, err := createTranscript(filepath.Join(dir, info.SessionID+".md"), info, "")
	if err != nil {
		return nil, err
	}
	w.full = full

	// One file per agent, holding everything shown in that agent's pane: what
	// the human said to it, what it said back, and what it exchanged with its
	// peer. That is the agent's own view of the round.
	for _, agent := range []string{"austin", "tony"} {
		f, err := createTranscript(filepath.Join(dir, info.SessionID+"-"+agent+".md"), info, agent)
		if err != nil {
			w.Close()
			return nil, err
		}
		w.agents[agent] = f
	}
	return w, nil
}

func createTranscript(path string, info Transcript, agent string) (*os.File, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	title := "Duo session " + info.SessionID
	if agent != "" {
		title += " — " + strings.ToUpper(agent[:1]) + agent[1:]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", title)
	b.WriteString("- Started: " + time.Now().Format("2006-01-02 15:04:05 -07:00") + "\n")
	fmt.Fprintf(&b, "- Repository: %s\n", info.Repository)
	if info.Branch != "" {
		fmt.Fprintf(&b, "- Branch: %s\n", info.Branch)
	}
	if info.BaseCommit != "" {
		fmt.Fprintf(&b, "- Base commit: %s\n", info.BaseCommit)
	}
	fmt.Fprintf(&b, "- Mode: %s\n", orUnknown(info.Mode))
	if agent != "" {
		fmt.Fprintf(&b, "- Agent: %s\n", strings.ToUpper(agent[:1])+agent[1:])
		b.WriteString("\nEvery entry below involves this agent: what the human asked of it, what it said back, and what it exchanged with its peer, in the order it appeared.\n")
	} else {
		b.WriteString("\nEvery line the Duo interface displayed during this session, in the order it appeared.\n")
	}
	b.WriteString("\n## Timeline\n")
	if _, err := f.WriteString(b.String()); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func orUnknown(s string) string {
	if s = strings.TrimSpace(s); s != "" {
		return s
	}
	return "unknown"
}

// Append writes one displayed entry to the transcript and, when the entry
// belongs to an agent's pane, to that agent's own transcript as well.
//
// The entry's text is already Markdown: the interface renders it as Markdown,
// so writing it through unchanged preserves an agent's code blocks and lists
// instead of flattening them.
func (w *LogWriter) Append(e TUIEntry) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	at := e.Time
	if at.IsZero() {
		at = time.Now()
	}
	// The journal stores UTC; a person reading a transcript wants their own
	// clock, which is also what the interface shows.
	stamp := at.Local().Format("15:04:05")

	header := "### " + stamp + " · " + e.header()
	if e.Error {
		header += " · error"
	} else if e.Warning {
		header += " · warning"
	}

	body := "\n" + header + "\n\n" + strings.TrimRight(e.Text, "\n") + "\n"

	if w.full != nil {
		_, _ = w.full.WriteString(body)
	}
	for _, agent := range w.agentsFor(e) {
		if f, ok := w.agents[agent]; ok && f != nil {
			_, _ = f.WriteString(body)
		}
	}
}

// agentsFor reports which agents' own transcripts an entry belongs to.
//
// A pane holds what its speaker said, not what the speaker was told: the
// interface files "Human → Austin" under Duo, because the human is the speaker.
// Selecting by pane alone would therefore keep only an agent's outbound
// messages and silently drop its task and its peer's replies.
//
// An entry belongs to an agent when that agent is either end of the message, so
// a message between the two agents lands in both logs: each of them received
// it. A pane with no label is a notice about whoever owns the pane.
func (w *LogWriter) agentsFor(e TUIEntry) []string {
	label := strings.ToLower(strings.TrimSpace(e.Label))
	var out []string
	for _, name := range []string{"austin", "tony"} {
		if strings.Contains(label, name) {
			out = append(out, name)
		}
	}
	if len(out) > 0 {
		return out
	}
	if pane := strings.ToLower(strings.TrimSpace(e.Pane)); pane == "austin" || pane == "tony" {
		return []string{pane}
	}
	return nil
}

// header reproduces the timeline label the interface shows. A directed pair
// such as "Austin → Tony" is recorded as such; a plain notice falls back to the
// speaker, which is what the interface does with an empty label too.
func (e TUIEntry) header() string {
	if label := strings.TrimSpace(e.Label); label != "" {
		return label
	}
	switch pane := strings.ToLower(strings.TrimSpace(e.Pane)); pane {
	case "austin":
		return "Austin"
	case "tony":
		return "Tony"
	case "duo":
		return "Duo"
	case "":
		return "Duo"
	default:
		return e.Pane
	}
}

// Close flushes and closes the transcripts. It is safe to call more than once.
func (w *LogWriter) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	var firstErr error
	closeOne := func(f *os.File) {
		if f == nil {
			return
		}
		if err := f.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	closeOne(w.full)
	w.full = nil
	for name, f := range w.agents {
		closeOne(f)
		delete(w.agents, name)
	}
	return firstErr
}
