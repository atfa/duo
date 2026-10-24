package delivery

import (
	"bytes"
	"context"
	"errors"
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

// refExists reports whether a ref resolves. It is a read-only query, so it can
// run before Duo decides to touch anything.
func refExists(ctx context.Context, dir, ref string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--verify", "--quiet", ref)
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return false, nil
		}
		return false, fmt.Errorf("git rev-parse --verify %s: %w", ref, err)
	}
	return true, nil
}

// gitConfig reads one configuration value. An unset key is reported as an empty
// value, not as an error, because "no upstream" is a normal state.
func gitConfig(ctx context.Context, dir, key string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "config", "--get", key)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return "", nil
		}
		return "", fmt.Errorf("git config --get %s: %s", key, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// remoteExists reports whether the repository has a remote with this name.
func remoteExists(ctx context.Context, dir, remote string) (bool, error) {
	out, err := gitOutput(ctx, dir, "remote")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == remote {
			return true, nil
		}
	}
	return false, nil
}

// lsRemote reads a branch from the remote without fetching or pushing anything.
func lsRemote(ctx context.Context, dir, remote, ref string) (string, error) {
	out, err := gitOutput(ctx, dir, "ls-remote", "--heads", remote, ref)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.TrimSpace(fields[1]) == strings.TrimSpace(ref) {
			return strings.TrimSpace(fields[0]), nil
		}
	}
	return "", nil
}

// objectPresent reports whether this repository has the commit object. A remote
// head it does not have is a remote that moved ahead of it.
func objectPresent(ctx context.Context, dir, sha string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "cat-file", "-e", sha+"^{commit}")
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && (exit.ExitCode() == 1 || exit.ExitCode() == 128) {
			return false, nil
		}
		return false, fmt.Errorf("git cat-file -e %s: %w", short(sha), err)
	}
	return true, nil
}
