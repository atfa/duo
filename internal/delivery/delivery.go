// Package delivery hands the final Duo artifact back to the repository the user
// launched Duo from.
//
// Duo keeps two things separate on purpose: the agents agree on a final
// artifact (INTEGRATE), and Duo Core then delivers that exact artifact to the
// user's original branch (DONE). Delivery is deliberately conservative: it only
// ever fast-forwards a clean, recorded branch, and it refuses to touch anything
// else. Git is the ground truth, and every operation here is idempotent so a
// crash can be reconciled on resume.
package delivery

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Manager describes one delivery target and the artifact to deliver.
type Manager struct {
	Repository  string
	BaseBranch  string
	BaseCommit  string
	FinalBranch string
	FinalHead   string
}

// Check is the result of inspecting the user's original repository before any
// delivery is attempted. It never mutates the repository.
type Check struct {
	Repository     string
	CurrentBranch  string
	CurrentHead    string
	BaseBranch     string
	BaseCommit     string
	FinalHead      string
	Clean          bool
	Detached       bool
	AlreadyApplied bool
	CanFastForward bool
	Reason         string
}

// Change is one entry of `git diff --name-status base..final`.
type Change struct {
	Status string
	Path   string
}

func (c Change) String() string {
	return fmt.Sprintf("%s\t%s", c.Status, c.Path)
}

// Result is the outcome of Deliver.
type Result struct {
	Check   Check
	Applied bool
	Changes []Change
}

// Check inspects the original repository without modifying it. Auto-delivery is
// only allowed when the repository is clean, on the recorded branch, and the
// final artifact can be reached by a fast-forward.
func (m Manager) Check(ctx context.Context) (Check, error) {
	check := Check{
		Repository: strings.TrimSpace(m.Repository),
		BaseBranch: strings.TrimSpace(m.BaseBranch),
		BaseCommit: strings.TrimSpace(m.BaseCommit),
		FinalHead:  strings.TrimSpace(m.FinalHead),
	}

	if check.Repository == "" {
		return check, errors.New("the original repository is unknown")
	}
	if check.FinalHead == "" {
		return check, errors.New("the final Duo artifact is unknown")
	}

	head, err := revParse(ctx, check.Repository, "HEAD")
	if err != nil {
		return check, err
	}
	check.CurrentHead = head

	branch, err := currentBranch(ctx, check.Repository)
	if err != nil {
		return check, err
	}
	check.Detached = branch == ""
	check.CurrentBranch = branch
	if check.Detached {
		check.CurrentBranch = "(detached)"
	}

	porcelain, err := gitOutput(ctx, check.Repository, "status", "--porcelain")
	if err != nil {
		return check, err
	}
	check.Clean = strings.TrimSpace(porcelain) == ""

	// The exact artifact is already present: delivery is a no-op. This is the
	// idempotency anchor that makes crash recovery safe.
	if head == check.FinalHead {
		check.AlreadyApplied = true
		return check, nil
	}

	ancestor, err := isAncestor(ctx, check.Repository, head, check.FinalHead)
	if err != nil {
		return check, err
	}
	baseAncestor := true
	if check.BaseCommit != "" {
		baseAncestor, err = isAncestor(ctx, check.Repository, check.BaseCommit, check.FinalHead)
		if err != nil {
			return check, err
		}
	}

	switch {
	case !ancestor:
		check.Reason = fmt.Sprintf(
			"original HEAD %s has diverged from the final Duo result %s; refusing to merge automatically",
			short(head), short(check.FinalHead),
		)
	case !baseAncestor:
		check.Reason = fmt.Sprintf(
			"final Duo result %s is not derived from the recorded base commit %s; refusing to deliver",
			short(check.FinalHead), short(check.BaseCommit),
		)
	case check.Detached:
		check.Reason = "the original repository is on a detached HEAD; refusing to move it automatically"
	case check.BaseBranch == "":
		check.Reason = "the branch Duo started on is unknown; refusing to move the original repository automatically"
	case check.CurrentBranch != check.BaseBranch:
		check.Reason = fmt.Sprintf(
			"the original repository is on branch %q but Duo started on %q; refusing to switch branches",
			check.CurrentBranch, check.BaseBranch,
		)
	case !check.Clean:
		check.Reason = "the original repository has uncommitted changes; refusing to overwrite them"
	default:
		check.CanFastForward = true
	}

	return check, nil
}

// Deliver checks the original repository and, when it is safe, fast-forwards it
// to the final artifact. A repository that cannot be delivered automatically is
// reported through Result.Applied == false and Check.Reason; the user's files
// are never modified in that case.
func (m Manager) Deliver(ctx context.Context) (Result, error) {
	check, err := m.Check(ctx)
	if err != nil {
		return Result{}, err
	}

	result := Result{Check: check}
	if check.AlreadyApplied {
		result.Applied = true
		result.Changes, _ = m.ChangedPaths(ctx)
		return result, nil
	}
	if !check.CanFastForward {
		return result, nil
	}

	if err := fastForward(ctx, check.Repository, check.FinalHead); err != nil {
		return result, err
	}

	head, err := revParse(ctx, check.Repository, "HEAD")
	if err != nil {
		return result, err
	}
	if head != check.FinalHead {
		return result, fmt.Errorf(
			"fast-forward reported success but the original repository is at %s instead of %s",
			short(head), short(check.FinalHead),
		)
	}

	result.Check.CurrentHead = head
	result.Applied = true
	result.Changes, _ = m.ChangedPaths(ctx)
	return result, nil
}

// ChangedPaths lists the files the final artifact adds, modifies, renames or
// deletes relative to the recorded base commit. It is diagnostic only.
func (m Manager) ChangedPaths(ctx context.Context) ([]Change, error) {
	base := strings.TrimSpace(m.BaseCommit)
	final := strings.TrimSpace(m.FinalHead)
	if base == "" || final == "" || base == final {
		return nil, nil
	}

	out, err := gitOutput(ctx, strings.TrimSpace(m.Repository), "diff", "--name-status", base, final)
	if err != nil {
		return nil, err
	}

	var changes []Change
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(fields) < 2 {
			continue
		}
		change := Change{Status: strings.TrimSpace(fields[0])}
		if len(fields) >= 3 {
			change.Path = fields[1] + " -> " + fields[2]
		} else {
			change.Path = fields[1]
		}
		changes = append(changes, change)
	}
	return changes, nil
}

// Summary renders changes as a short, log-friendly block.
func Summary(changes []Change) string {
	if len(changes) == 0 {
		return "  (no file changes relative to the recorded base commit)"
	}
	const limit = 20
	var b strings.Builder
	for i, change := range changes {
		if i == limit {
			b.WriteString("  … and " + strconv.Itoa(len(changes)-i) + " more\n")
			break
		}
		b.WriteString("  " + change.String() + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// BranchHead resolves a branch in the repository that owns the Duo worktrees.
// It is the fallback when a legacy snapshot lost its integration head.
func BranchHead(ctx context.Context, repository, branch string) (string, error) {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return "", errors.New("branch name is empty")
	}
	return revParse(ctx, repository, "refs/heads/"+branch)
}

func short(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 10 {
		return value[:10]
	}
	return value
}
