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

// chooseResumeSession prompts for one of several available unfinished sessions.
// It is only called by the CLI when a controlling terminal is available; batch
// callers retain the session service's existing diagnostic.
func chooseResumeSession(items []sessionstore.Summary, in io.Reader, out io.Writer) (string, error) {
	if len(items) < 2 {
		return "", nil
	}
	fmt.Fprintln(out, "Multiple unfinished Duo sessions:")
	for i, item := range items {
		fmt.Fprintf(out, "  %d) %s  phase=%s  scope=%s  updated=%s\n",
			i+1, item.SessionID, item.Snapshot.Phase, session.EffectiveScope(item.Snapshot.ScopePath), item.Snapshot.UpdatedAt.Local().Format(time.RFC3339))
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
		} else if item.Unfinished() && !sessionIsActive(item.Dir) {
			available = append(available, item)
		}
	}
	return available, broken
}
