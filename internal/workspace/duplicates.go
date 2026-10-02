package workspace

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/atfa/duo/internal/protocol"
)

// DuplicateSide is one agent's copy of a change that both branches carry.
type DuplicateSide struct {
	Agent protocol.AgentID
	// SHA is the commit holding this copy.
	SHA string
	// Subject is that commit's own first line.
	Subject string
	// Committed is its commit time, used only to suggest which copy came second.
	Committed time.Time
}

// DuplicatePatch is a single change present on both agent branches under
// different SHAs, which is what a cherry-pick leaves behind.
type DuplicatePatch struct {
	// Sides holds one entry per agent, oldest commit first.
	Sides []DuplicateSide
}

// DuplicateCommits reports changes that both agents' branches carry.
//
// The two branches are merged at INTEGRATE, so a commit that exists only because
// an agent cherry-picked its peer's work is redundant in the delivered history:
// the merge brings the change across anyway, while the copy makes `git revert`
// ambiguous — reverting either copy removes the change even though the other
// still claims to add it — and hands `git bisect` commits that change nothing.
//
// Changes are matched by patch id, not by SHA, so a cherry-pick that landed at
// a different point in the branch, or whose message was rewritten, is still
// recognised. Git does not record which of two identical patches came first, so
// the pair is reported rather than attributed: only the agent that ran
// cherry-pick knows which copy is theirs.
func (m *GitManager) DuplicateCommits(ctx context.Context) ([]DuplicatePatch, error) {
	set := m.set
	if set.Repository == "" {
		return nil, nil
	}

	// patch id -> agent -> that agent's copy.
	groups := map[string]map[protocol.AgentID]DuplicateSide{}

	for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		own, ok := set.For(agent)
		if !ok {
			continue
		}
		peer, ok := set.For(protocol.PeerOf(agent))
		if !ok {
			continue
		}
		// `git cherry <peer> <own>` lists every commit on <own> that <peer>
		// lacks, marking with '-' the ones whose patch the peer already has.
		raw, err := gitOutput(ctx, set.Repository, "cherry", peer.Branch, own.Branch)
		if err != nil {
			// A branch that does not exist yet simply has nothing to compare.
			continue
		}
		for _, sha := range cherryMinusSHAs(raw) {
			side, ok := m.duplicateSide(ctx, set.Repository, agent, sha)
			if !ok {
				continue
			}
			if groups[side.patchID] == nil {
				groups[side.patchID] = map[protocol.AgentID]DuplicateSide{}
			}
			groups[side.patchID][agent] = side.side
		}
	}

	var out []DuplicatePatch
	for _, byAgent := range groups {
		// One agent's own work is not a duplicate; it needs a copy on the peer
		// branch to be redundant.
		if len(byAgent) < 2 {
			continue
		}
		patch := DuplicatePatch{}
		for _, side := range byAgent {
			patch.Sides = append(patch.Sides, side)
		}
		sort.Slice(patch.Sides, func(i, j int) bool {
			if !patch.Sides[i].Committed.Equal(patch.Sides[j].Committed) {
				return patch.Sides[i].Committed.Before(patch.Sides[j].Committed)
			}
			return patch.Sides[i].Agent < patch.Sides[j].Agent
		})
		out = append(out, patch)
	}
	// Stable order so the message does not shuffle between the two agents that
	// receive it.
	sort.Slice(out, func(i, j int) bool {
		return out[i].Sides[0].SHA < out[j].Sides[0].SHA
	})
	return out, nil
}

// cherryMinusSHAs extracts the commits `git cherry` marked as already present
// upstream. Depending on the Git version the line is either "- <sha>" or
// "- <sha> (cherry picked from commit <sha>)", so the SHA is taken as the first
// field and nothing after it is trusted.
func cherryMinusSHAs(cherryOutput string) []string {
	var shas []string
	for _, line := range strings.Split(cherryOutput, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		sha, _, _ := strings.Cut(strings.TrimSpace(strings.TrimPrefix(line, "-")), " ")
		sha = strings.TrimSpace(sha)
		if len(sha) >= 7 && isHexSHA(sha) {
			shas = append(shas, sha)
		}
	}
	return shas
}

func isHexSHA(s string) bool {
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

type identifiedSide struct {
	patchID string
	side    DuplicateSide
}

// duplicateSide describes one commit: the stable id of the change it makes and
// the metadata a report needs. Any failure yields ok=false, because a commit
// that cannot be identified must not be guessed at.
func (m *GitManager) duplicateSide(ctx context.Context, dir string, agent protocol.AgentID, sha string) (identifiedSide, bool) {
	patchID, err := gitPatchID(ctx, dir, sha)
	if err != nil {
		return identifiedSide{}, false
	}
	raw, err := gitOutput(ctx, dir, "show", "-s", "--format=%s%x00%cI", sha)
	if err != nil {
		return identifiedSide{}, false
	}
	subject, when, _ := strings.Cut(strings.TrimSpace(raw), "\x00")
	committed, _ := time.Parse(time.RFC3339, strings.TrimSpace(when))
	return identifiedSide{
		patchID: patchID,
		side:    DuplicateSide{Agent: agent, SHA: sha, Subject: subject, Committed: committed},
	}, true
}

// gitPatchID returns the stable patch id of a commit. git patch-id reads a diff
// on stdin and prints nothing when handed only a commit, so the diff is produced
// first; diff-tree is used rather than show because it emits the bare patch
// without the commit header.
func gitPatchID(ctx context.Context, dir, sha string) (string, error) {
	patch, err := gitOutput(ctx, dir, "diff-tree", "-p", sha)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(patch) == "" {
		// A merge or an empty commit has no single patch to identify.
		return "", fmt.Errorf("no patch for %s", sha)
	}
	cmd := exec.CommandContext(ctx, "git", "patch-id", "--stable")
	cmd.Stdin = strings.NewReader(patch)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	id, _, _ := strings.Cut(strings.TrimSpace(out.String()), " ")
	id = strings.TrimSpace(id)
	if len(id) < 8 {
		return "", fmt.Errorf("unusable patch id %q for %s", id, sha)
	}
	return id, nil
}

// DuplicateCommitNotice renders the report sent to both agents when the
// EXECUTE → REVIEW transition finds a change carried by both branches.
//
// Nothing is signed at this point, so an agent can still drop the copy without
// invalidating a signature. Duo reports rather than rewrites: the branch belongs
// to the agent, and silently rebasing an agent's history would contradict the
// evidence binding that makes the rest of the session auditable.
func DuplicateCommitNotice(dupes []DuplicatePatch) string {
	if len(dupes) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\n\n[Duo: %d %s carried by both branches]\n", len(dupes), plural(len(dupes), "change is", "changes are"))
	b.WriteString("Duo merges your branch into your peer's at INTEGRATE, so a commit that exists only because one of you cherry-picked the other's work is redundant. It makes `git revert` ambiguous, because reverting either copy removes the change while the other still claims to add it, and it gives `git bisect` commits that change nothing.\n\n")
	for _, d := range dupes {
		b.WriteString("The same change is committed on both branches:\n")
		for i, s := range d.Sides {
			role := "earlier"
			switch {
			case i == len(d.Sides)-1 && !s.Committed.Equal(d.Sides[0].Committed):
				role = "later, so most likely the copy"
			case len(d.Sides) > 1 && s.Committed.Equal(d.Sides[0].Committed):
				role = "same commit time as the other copy"
			}
			when := ""
			if !s.Committed.IsZero() {
				when = s.Committed.Local().Format("2006-01-02 15:04:05")
			}
			fmt.Fprintf(&b, "  %-6s  %-8s  %-44s  %s %s\n", s.Agent, shortSHAOf(s.SHA), truncate(s.Subject, 44), role, when)
		}
		b.WriteString("\n")
	}
	b.WriteString("If one of these is a cherry-pick of the other, the agent who ran it should drop that copy from its own branch before signing REVIEW — for example an interactive rebase that drops the commit. The tree does not change, only the history.\n")
	b.WriteString("Git does not record which identical patch came first, so Duo cannot name the owner: each of you knows whether you cherry-picked. If a change legitimately exists on both branches because you wrote it independently, say so and keep both.\n")
	b.WriteString("Duo does not rewrite agent branches. If dropping a commit turns out to be wrong, keep it and say so: a redundant commit is much better than lost work.\n")
	b.WriteString("No signature is bound to either branch yet, so this is the cheapest moment to settle it.")
	return b.String()
}

// truncate keeps the report's columns aligned when a commit message is long.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func shortSHAOf(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
