package main

import (
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
