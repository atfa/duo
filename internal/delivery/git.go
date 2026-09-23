package delivery

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// gitOutput runs a read-only git command in dir and returns its stdout.
func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), message)
	}
	return stdout.String(), nil
}

// revParse resolves ref to a commit SHA.
func revParse(ctx context.Context, dir, ref string) (string, error) {
	out, err := gitOutput(ctx, dir, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// currentBranch returns the checked-out branch, or "" for a detached HEAD.
func currentBranch(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "symbolic-ref", "--short", "-q", "HEAD")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// `symbolic-ref -q` exits non-zero with no output on a detached HEAD.
		if strings.TrimSpace(stderr.String()) == "" {
			return "", nil
		}
		return "", fmt.Errorf("git symbolic-ref HEAD: %s", strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// isAncestor reports whether ancestor is reachable from descendant.
func isAncestor(ctx context.Context, dir, ancestor, descendant string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "merge-base", "--is-ancestor", ancestor, descendant)
	if err := cmd.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
			return false, nil
		}
		return false, fmt.Errorf("git merge-base --is-ancestor %s %s: %w", short(ancestor), short(descendant), err)
	}
	return true, nil
}

// fastForward moves the current branch to target without creating a merge
// commit. It is only ever called after Check proved that this is a safe
// fast-forward of a clean branch.
func fastForward(ctx context.Context, dir, target string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "merge", "--ff-only", target)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = strings.TrimSpace(stdout.String())
		}
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("git merge --ff-only %s: %s", short(target), message)
	}
	return nil
}
