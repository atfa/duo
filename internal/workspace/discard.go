package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/atfa/duo/internal/protocol"
)

// CarriesWork reports whether either agent's worktree holds anything the
// session's own starting point did not already have: a commit on top of the base
// commit, or edits that were never committed.
//
// It exists because "the human never submitted a task" is a claim about the
// session, and it is only as good as the drivers' reporting: a driver that never
// says an agent started a turn leaves a session looking untouched even when the
// human attached to an agent and worked in it by hand. Asking Git instead of
// trusting the claim is what makes discarding safe for a driver nobody has
// audited, and it costs two commands per worktree.
func (s Set) CarriesWork(ctx context.Context) (bool, error) {
	for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		wt, ok := s.For(agent)
		if !ok || strings.TrimSpace(wt.Path) == "" {
			continue
		}
		dirty, err := gitOutput(ctx, wt.Path, "status", "--porcelain")
		if err != nil {
			// An unreadable worktree is not a clean one. Reporting it as clean is
			// the one answer that could lose someone's work.
			return true, err
		}
		if strings.TrimSpace(dirty) != "" {
			return true, nil
		}
		base := strings.TrimSpace(s.BaseCommit)
		if base == "" {
			continue
		}
		head, err := gitOutput(ctx, wt.Path, "rev-parse", "HEAD")
		if err != nil || strings.TrimSpace(head) == "" {
			return true, err
		}
		if strings.TrimSpace(head) == base {
			continue
		}
		ahead, err := gitOutput(ctx, wt.Path, "rev-list", "--count", base+"..HEAD")
		if err != nil {
			return true, err
		}
		if strings.TrimSpace(ahead) != "0" {
			return true, nil
		}
	}
	return false, nil
}

// Discard removes the worktrees, prunes them from Git, and deletes the session's
// branches. It is the Git half of `duo clean`, moved here so that ending a
// session and cleaning up after it are one implementation rather than two that
// can drift.
//
// Nothing here is undoable, which is why CarriesWork exists: the caller decides
// whether there is anything worth keeping, and this only performs the removal.
func (s Set) Discard(ctx context.Context) error {
	home, _ := os.UserHomeDir()
	duoWorktreesBase := filepath.Join(home, ".duo", "worktrees")

	for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		wt, ok := s.For(agent)
		if !ok || wt.Path == "" {
			continue
		}
		if strings.TrimSpace(s.Repository) != "" {
			_ = exec.CommandContext(ctx, "git", "-C", s.Repository, "worktree", "remove", "--force", wt.Path).Run()
		}
	}
	if strings.TrimSpace(s.Repository) != "" {
		_ = exec.CommandContext(ctx, "git", "-C", s.Repository, "worktree", "prune").Run()
		for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
			wt, ok := s.For(agent)
			if !ok || strings.TrimSpace(wt.Branch) == "" {
				continue
			}
			_ = exec.CommandContext(ctx, "git", "-C", s.Repository, "branch", "-D", wt.Branch).Run()
		}
	}

	// The directory on disk, for the case where Git could not remove the
	// worktree itself. Guarded on the same base duo clean has always used: a
	// path outside it is never removed, whatever asked.
	sessionWorktreeDir := ""
	if wt, ok := s.For(protocol.Austin); ok && wt.Path != "" {
		sessionWorktreeDir = filepath.Dir(wt.Path)
	} else if wt, ok := s.For(protocol.Tony); ok && wt.Path != "" {
		sessionWorktreeDir = filepath.Dir(wt.Path)
	}
	if sessionWorktreeDir != "" && safeWorktreeSubdir(sessionWorktreeDir, duoWorktreesBase) {
		_ = os.RemoveAll(sessionWorktreeDir)
		parentRepoWorktreeDir := filepath.Dir(sessionWorktreeDir)
		if safeWorktreeSubdir(parentRepoWorktreeDir, duoWorktreesBase) {
			_ = os.Remove(parentRepoWorktreeDir)
		}
	}
	return nil
}

// safeWorktreeSubdir reports whether path is strictly inside base, so a removal
// can never reach the base itself or anything beside or above it.
func safeWorktreeSubdir(path, base string) bool {
	if path == "" || base == "" {
		return false
	}
	cleanPath := filepath.Clean(path)
	cleanBase := filepath.Clean(base)
	if cleanPath == cleanBase {
		return false
	}
	rel, err := filepath.Rel(cleanBase, cleanPath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}
