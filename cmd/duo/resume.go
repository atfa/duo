package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/atfa/duo/internal/session"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/atfa/duo/internal/workspace"
)

// The composition root moved to internal/session: recovery, worktree preparation,
// the session lock and delivery now live there, so a frontend other than this CLI
// does not have to reimplement them.
//
// The names below stay because `duo apply` and `duo sessions` are their own
// entry points, and they must share exactly one implementation with the session
// rather than a second copy that could drift.

// resumeSessionRequest is what runWith returns when the human picked another
// session in /sessions: the current session was closed and preserved, and run
// resumes the chosen id in this same process, on exactly the path
// `duo resume <id>` takes. It is an error so it travels the same way every other
// way a session ends does.
type resumeSessionRequest struct{ SessionID string }

func (*resumeSessionRequest) Error() string { return "duo session switch requested" }

// resumedSession is the configuration of the session /sessions picks. Its mode
// comes from that session's persisted state, so nothing here overrides it; what a
// resume needs from the launch configuration is the resume flag, the id and the
// harness grace period that keeps a reconnecting agent from looking stalled.
//
// The launch-time mode override is dropped. `--mode goal` belongs to the session
// the human started, not to a session picked from the list: keeping it would make
// resumeMode refuse a target persisted as FAST, and the picker would fail after
// this session was already closed. Dropping it is what `duo resume <id>` does
// without a flag.
func (c config) resumedSession(id string) config {
	c.resume = true
	c.resumeSession = id
	c.mode, c.modeRaw, c.modeSource, c.modeExplicit = "", "", "", false
	c.harness.RecoveryGrace = resumeRecoveryGrace()
	return c
}

// setFromSnapshot rebuilds the workspace set from persisted state.
func setFromSnapshot(snap sessionstore.Snapshot) workspace.Set {
	return session.SetFromSnapshot(snap)
}

// formatBroken renders the stored sessions that could not be read.
func formatBroken(items []sessionstore.Summary) string {
	return session.FormatBroken(items)
}

// formatCandidates renders the stored sessions a resume could choose between.
func formatCandidates(items []sessionstore.Summary) string {
	return session.FormatCandidates(items)
}

// chooseResumeSession prompts for one of several available sessions.
// It is only called by the CLI when a controlling terminal is available; batch
// callers retain the session service's existing diagnostic.
func chooseResumeSession(items []sessionstore.Summary, in io.Reader, out io.Writer) (string, error) {
	if len(items) < 2 {
		return "", nil
	}
	fmt.Fprintln(out, "Multiple Duo sessions:")
	for i, item := range items {
		status := "unfinished"
		if item.Snapshot.Phase == "DONE" {
			status = "completed"
		}
		fmt.Fprintf(out, "  %d) %s  status=%s  phase=%s  scope=%s  updated=%s\n",
			i+1, item.Label(), status, item.Snapshot.Phase, session.EffectiveScope(item.Snapshot.ScopePath), item.Snapshot.UpdatedAt.Local().Format(time.RFC3339))
	}
	fmt.Fprint(out, "Choose a session number (or session ID): ")
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	choice := strings.TrimSpace(line)
	if choice == "" {
		return "", fmt.Errorf("no session selected")
	}
	for i, item := range items {
		if choice == item.SessionID || choice == fmt.Sprint(i+1) {
			return item.SessionID, nil
		}
	}
	return "", fmt.Errorf("invalid session choice %q", choice)
}

func maybeChooseResumeSession(ctx context.Context, cfg *config) error {
	if !cfg.resume || cfg.resumeSession != "" {
		return nil
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil // Preserve the service's useful non-interactive error.
	}
	defer tty.Close()
	root, err := workspace.FindRoot(ctx, cfg.launchDir)
	if err != nil {
		return err
	}
	baseDir, err := sessionstore.DefaultBaseDir()
	if err != nil {
		return err
	}
	items, err := sessionstore.List(baseDir, sessionstore.RepoID(root))
	if err != nil {
		return err
	}
	available, broken := resumeChoices(items)
	if len(available) > 0 {
		writeResumeDiagnostics(tty, broken)
	}
	if len(available) < 2 {
		return nil
	}
	selected, err := chooseResumeSession(available, tty, tty)
	if err != nil {
		return err
	}
	cfg.resumeSession = selected
	return nil
}

func writeResumeDiagnostics(out io.Writer, broken []sessionstore.Summary) {
	if len(broken) > 0 {
		fmt.Fprintf(out, "Unreadable Duo session(s):\n%s\n", formatBroken(broken))
	}
}

func resumeChoices(items []sessionstore.Summary) (available, broken []sessionstore.Summary) {
	for _, item := range items {
		if item.Err != nil {
			broken = append(broken, item)
		} else if !sessionIsActive(item.Dir) {
			available = append(available, item)
		}
	}
	return available, broken
}
