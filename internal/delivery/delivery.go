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

	// Branch, when set, lands the artifact on that new branch instead of moving
	// the branch Duo started on. The user's checkout, its HEAD and its index are
	// left alone, which is what a protected-branch workflow needs.
	Branch string
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

	// The final artifact is already part of the current history: delivery is a
	// no-op. This also recognizes a user-resolved non-fast-forward merge.
	// It is the idempotency anchor that makes crash recovery safe.
	alreadyApplied, err := isAncestor(ctx, check.Repository, check.FinalHead, head)
	if err != nil {
		return check, err
	}
	if alreadyApplied {
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

// Deliver checks the original repository and, when it is safe, lands the
// artifact: a new branch when Manager.Branch names one, otherwise a
// fast-forward of the branch Duo started on. A repository that cannot be
// delivered is reported through Result.Applied == false and Check.Reason; the
// user's files are never modified in that case.
func (m Manager) Deliver(ctx context.Context) (Result, error) {
	if branch := strings.TrimSpace(m.Branch); branch != "" {
		return m.deliverToNewBranch(ctx, branch)
	}

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

// deliverToNewBranch creates branch at the final artifact. It is deliberately a
// ref write and nothing else: the checkout, its HEAD and its index stay as they
// were, so a dirty tree or a protected current branch cannot make the handoff
// unsafe. Duo never forces the ref — a branch that already exists at another
// commit is refused, and one that already points at the artifact is the
// idempotent no-op.
func (m Manager) deliverToNewBranch(ctx context.Context, branch string) (Result, error) {
	check := Check{
		Repository: strings.TrimSpace(m.Repository),
		BaseBranch: strings.TrimSpace(m.BaseBranch),
		BaseCommit: strings.TrimSpace(m.BaseCommit),
		FinalHead:  strings.TrimSpace(m.FinalHead),
	}
	result := Result{Check: check}
	refuse := func(reason string) (Result, error) {
		check.Reason = reason
		result.Check = check
		return result, nil
	}

	if check.Repository == "" {
		return result, errors.New("the original repository is unknown")
	}
	if check.FinalHead == "" {
		return result, errors.New("the final Duo artifact is unknown")
	}
	if branch == check.BaseBranch {
		return refuse(fmt.Sprintf("branch %q is the branch Duo started on; --branch must name a different branch", branch))
	}
	if err := validBranchName(ctx, check.Repository, branch); err != nil {
		return refuse(err.Error())
	}

	final, err := revParse(ctx, check.Repository, check.FinalHead)
	if err != nil {
		return result, err
	}
	check.FinalHead = final
	if check.BaseCommit != "" {
		derived, err := isAncestor(ctx, check.Repository, check.BaseCommit, final)
		if err != nil {
			return result, err
		}
		if !derived {
			return refuse(fmt.Sprintf(
				"final Duo result %s is not derived from the recorded base commit %s; refusing to deliver",
				short(final), short(check.BaseCommit),
			))
		}
	}

	exists, err := refExists(ctx, check.Repository, "refs/heads/"+branch)
	if err != nil {
		return result, err
	}
	if exists {
		head, err := revParse(ctx, check.Repository, "refs/heads/"+branch)
		if err != nil {
			return result, err
		}
		if head != final {
			return refuse(fmt.Sprintf(
				"branch %q already exists at %s; refusing to move it to %s",
				branch, short(head), short(final),
			))
		}
		check.AlreadyApplied = true
	} else {
		if _, err := gitOutput(ctx, check.Repository, "branch", branch, final); err != nil {
			return result, err
		}
		head, err := revParse(ctx, check.Repository, "refs/heads/"+branch)
		if err != nil {
			return result, err
		}
		if head != final {
			return result, fmt.Errorf(
				"branch %s was created at %s instead of %s",
				branch, short(head), short(final),
			)
		}
	}

	check.CurrentHead = final
	result.Check = check
	result.Applied = true
	result.Changes, _ = m.ChangedPaths(ctx)
	return result, nil
}

// validBranchName rejects a name Git would refuse, or one that would be read as
// a flag on the command line.
func validBranchName(ctx context.Context, dir, branch string) error {
	if branch == "" {
		return errors.New("--branch needs a branch name")
	}
	if strings.HasPrefix(branch, "-") {
		return fmt.Errorf("branch %q must not start with '-'", branch)
	}
	if _, err := gitOutput(ctx, dir, "check-ref-format", "refs/heads/"+branch); err != nil {
		return fmt.Errorf("branch %q is not a valid branch name: %w", branch, err)
	}
	return nil
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

// PushCheck is the read-only verdict before a delivery pushes its branch. Duo
// proves the push cannot discard remote commits before anything moves, so a
// refused push leaves the local branch and the remote exactly as they were.
type PushCheck struct {
	Repository    string
	Branch        string
	Remote        string
	RemoteRef     string
	Head          string
	RemoteHead    string
	CanPush       bool
	AlreadyPushed bool
	Reason        string
}

// CheckPush resolves where branch would be pushed and compares that with head.
// It never mutates the repository or the remote.
func CheckPush(ctx context.Context, repository, branch, head string) (PushCheck, error) {
	check := PushCheck{
		Repository: strings.TrimSpace(repository),
		Branch:     strings.TrimSpace(branch),
		Head:       strings.TrimSpace(head),
	}
	if check.Repository == "" {
		return check, errors.New("the original repository is unknown")
	}
	if check.Branch == "" {
		return check, errors.New("the branch to push is unknown")
	}
	if check.Head == "" {
		return check, errors.New("the commit to push is unknown")
	}

	remote, err := gitConfig(ctx, check.Repository, "branch."+check.Branch+".remote")
	if err != nil {
		return check, err
	}
	ref, err := gitConfig(ctx, check.Repository, "branch."+check.Branch+".merge")
	if err != nil {
		return check, err
	}
	if remote == "" || ref == "" {
		// A branch Duo just created has no upstream yet. Pushing it to origin
		// under its own name is the same thing `git push -u origin <branch>`
		// would do, and it keeps --branch usable for a pull-request workflow.
		origin, err := remoteExists(ctx, check.Repository, "origin")
		if err != nil {
			return check, err
		}
		if !origin {
			check.Reason = fmt.Sprintf(
				"branch %q has no upstream and this repository has no origin remote; set one with `git push -u <remote> %s` and retry",
				check.Branch, check.Branch,
			)
			return check, nil
		}
		remote, ref = "origin", "refs/heads/"+check.Branch
	}
	check.Remote = remote
	check.RemoteRef = ref

	remoteHead, err := lsRemote(ctx, check.Repository, remote, ref)
	if err != nil {
		return check, err
	}
	check.RemoteHead = remoteHead
	switch {
	case remoteHead == "":
		check.CanPush = true
	case remoteHead == check.Head:
		check.AlreadyPushed = true
		check.CanPush = true
	default:
		present, err := objectPresent(ctx, check.Repository, remoteHead)
		if err != nil {
			return check, err
		}
		if !present {
			check.Reason = fmt.Sprintf(
				"%s has commits this repository does not have (%s); fetch and retry, Duo will not overwrite them",
				remoteRefName(remote, ref), short(remoteHead),
			)
			return check, nil
		}
		contained, err := isAncestor(ctx, check.Repository, remoteHead, check.Head)
		if err != nil {
			return check, err
		}
		if !contained {
			check.Reason = fmt.Sprintf(
				"%s has moved to %s, which is not contained in the final Duo result %s; refusing to push",
				remoteRefName(remote, ref), short(remoteHead), short(check.Head),
			)
			return check, nil
		}
		check.CanPush = true
	}
	return check, nil
}

// Push updates the upstream branch to the checked head. It is only called after
// CheckPush approved the push, and it re-reads the remote afterwards so a
// reported success is the remote's own answer, not the push command's.
func Push(ctx context.Context, check PushCheck) error {
	if !check.CanPush || check.AlreadyPushed {
		return nil
	}
	if _, err := gitOutput(ctx, check.Repository, "push", check.Remote, check.Head+":"+check.RemoteRef); err != nil {
		return err
	}
	remoteHead, err := lsRemote(ctx, check.Repository, check.Remote, check.RemoteRef)
	if err != nil {
		return err
	}
	if remoteHead != check.Head {
		return fmt.Errorf(
			"the push reported success but %s is at %s instead of %s",
			remoteRefName(check.Remote, check.RemoteRef), short(remoteHead), short(check.Head),
		)
	}
	return nil
}

// remoteRefName renders a remote branch the way a human names it: origin/main.
func remoteRefName(remote, ref string) string {
	return remote + "/" + strings.TrimPrefix(strings.TrimSpace(ref), "refs/heads/")
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
