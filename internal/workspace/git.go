package workspace

import (
	"bytes"
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/atfa/duo/internal/protocol"
)

type GitConfig struct {
	Repository string
	Root       string
	Session    string
	BaseRef    string
}

type GitManager struct {
	cfg GitConfig
	set Set
}

const initialCommitSteps = `  # Review or create .gitignore before adding files.
  git add .
  git commit --allow-empty -m "Initial commit"
  duo`

func NewGitManager(cfg GitConfig) *GitManager {
	return &GitManager{cfg: cfg}
}

func (m *GitManager) Set() Set { return m.set }

func (m *GitManager) Prepare(ctx context.Context) (Set, error) {
	repo := strings.TrimSpace(m.cfg.Repository)
	if repo == "" {
		repo = "."
	}

	root, err := gitOutput(ctx, repo, "rev-parse", "--show-toplevel")
	if err != nil {
		return Set{}, fmt.Errorf("Duo requires an existing Git repository and will not initialize one automatically.\n\nTo prepare this directory:\n  git init\n%s\n\nGit check failed: %w", initialCommitSteps, err)
	}
	root, err = filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		return Set{}, err
	}

	if dirty, err := gitOutput(ctx, root, "status", "--porcelain"); err != nil {
		return Set{}, err
	} else if strings.TrimSpace(dirty) != "" {
		return Set{}, fmt.Errorf("base repository is dirty; commit or stash changes before starting Duo worktrees:\n%s", dirty)
	}

	baseRef := strings.TrimSpace(m.cfg.BaseRef)
	defaultBaseRef := baseRef == "" || baseRef == "HEAD"
	if baseRef == "" {
		baseRef = "HEAD"
	}
	baseCommit, err := gitOutput(ctx, root, "rev-parse", baseRef+"^{commit}")
	if err != nil {
		if defaultBaseRef {
			return Set{}, fmt.Errorf("Duo requires at least one commit and will not create one automatically.\n\nTo prepare this repository:\n%s", initialCommitSteps)
		}
		return Set{}, fmt.Errorf("resolve base ref %q: %w", baseRef, err)
	}
	baseCommit = strings.TrimSpace(baseCommit)

	baseBranch, _ := gitOutput(ctx, root, "symbolic-ref", "--short", "HEAD")
	baseBranch = strings.TrimSpace(baseBranch)
	if baseBranch == "" {
		baseBranch = "(detached)"
	}

	session := sanitizeSession(m.cfg.Session)
	if session == "" {
		return Set{}, errors.New("session name is empty after sanitization")
	}

	workspaceRoot := strings.TrimSpace(m.cfg.Root)
	if workspaceRoot == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Set{}, err
		}
		h := sha1.Sum([]byte(root))
		repoName := filepath.Base(root)
		workspaceRoot = filepath.Join(home, ".duo", "worktrees", fmt.Sprintf("%s-%x", repoName, h[:4]), session)
	}
	workspaceRoot, err = filepath.Abs(workspaceRoot)
	if err != nil {
		return Set{}, err
	}
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		return Set{}, err
	}

	set := Set{
		Repository: root,
		BaseBranch: baseBranch,
		BaseCommit: baseCommit,
		Session:    session,
		Root:       workspaceRoot,
	}

	for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		branch := fmt.Sprintf("duo/%s/%s", session, strings.ToLower(string(agent)))
		path := filepath.Join(workspaceRoot, strings.ToLower(string(agent)))

		wt, err := m.ensureWorktree(ctx, root, baseCommit, agent, branch, path)
		if err != nil {
			return Set{}, err
		}
		switch agent {
		case protocol.Austin:
			set.Austin = wt
		case protocol.Tony:
			set.Tony = wt
		}
	}

	m.set = set
	return set, nil
}

func (m *GitManager) ensureWorktree(
	ctx context.Context,
	repo string,
	baseCommit string,
	agent protocol.AgentID,
	branch string,
	path string,
) (Worktree, error) {
	if st, err := os.Stat(path); err == nil && st.IsDir() {
		actualRoot, err := gitOutput(ctx, path, "rev-parse", "--show-toplevel")
		if err != nil || !SamePath(strings.TrimSpace(actualRoot), path) {
			return Worktree{}, fmt.Errorf("existing worktree path is not the expected Git worktree: %s", path)
		}
		actualBranch, err := gitOutput(ctx, path, "branch", "--show-current")
		if err != nil {
			return Worktree{}, err
		}
		if strings.TrimSpace(actualBranch) != branch {
			return Worktree{}, fmt.Errorf("existing worktree %s is on branch %q, expected %q", path, strings.TrimSpace(actualBranch), branch)
		}
		head, err := gitOutput(ctx, path, "rev-parse", "HEAD")
		if err != nil {
			return Worktree{}, err
		}
		return Worktree{Agent: agent, Path: path, Branch: branch, Head: strings.TrimSpace(head)}, nil
	}

	branchExists := exec.CommandContext(ctx, "git", "-C", repo, "show-ref", "--verify", "--quiet", "refs/heads/"+branch).Run() == nil
	var cmd *exec.Cmd
	if branchExists {
		cmd = exec.CommandContext(ctx, "git", "-C", repo, "worktree", "add", path, branch)
	} else {
		cmd = exec.CommandContext(ctx, "git", "-C", repo, "worktree", "add", "-b", branch, path, baseCommit)
	}
	if output, err := cmd.CombinedOutput(); err != nil {
		return Worktree{}, fmt.Errorf("create %s worktree: %w\n%s", agent, err, strings.TrimSpace(string(output)))
	}

	head, err := gitOutput(ctx, path, "rev-parse", "HEAD")
	if err != nil {
		return Worktree{}, err
	}
	return Worktree{Agent: agent, Path: path, Branch: branch, Head: strings.TrimSpace(head)}, nil
}

func (m *GitManager) Status(ctx context.Context, agent protocol.AgentID) (Status, error) {
	wt, ok := m.set.For(agent)
	if !ok {
		return Status{}, fmt.Errorf("unknown agent: %s", agent)
	}

	head, err := gitOutput(ctx, wt.Path, "rev-parse", "HEAD")
	if err != nil {
		return Status{}, err
	}
	porcelain, err := gitOutput(ctx, wt.Path, "status", "--porcelain")
	if err != nil {
		return Status{}, err
	}
	aheadText, err := gitOutput(ctx, wt.Path, "rev-list", "--count", m.set.BaseCommit+"..HEAD")
	if err != nil {
		return Status{}, err
	}
	ahead, _ := strconv.Atoi(strings.TrimSpace(aheadText))

	return Status{
		Agent:  agent,
		Path:   wt.Path,
		Branch: wt.Branch,
		Head:   strings.TrimSpace(head),
		Dirty:  strings.TrimSpace(porcelain) != "",
		Ahead:  ahead,
	}, nil
}

func (m *GitManager) CaptureArtifact(ctx context.Context, agent protocol.AgentID) (Artifact, error) {
	status, err := m.Status(ctx, agent)
	if err != nil {
		return Artifact{}, err
	}
	if status.Dirty {
		return Artifact{}, fmt.Errorf("%s worktree has uncommitted changes; commit them before marking this phase ready", agent)
	}
	return Artifact{
		Agent:      agent,
		Branch:     status.Branch,
		Commit:     status.Head,
		BaseCommit: m.set.BaseCommit,
		Ahead:      status.Ahead,
	}, nil
}

func (m *GitManager) IntegrateTonyIntoAustin(ctx context.Context) (IntegrationResult, error) {
	austin, err := m.Status(ctx, protocol.Austin)
	if err != nil {
		return IntegrationResult{}, err
	}
	tony, err := m.Status(ctx, protocol.Tony)
	if err != nil {
		return IntegrationResult{}, err
	}
	if austin.Dirty || tony.Dirty {
		return IntegrationResult{}, errors.New("cannot integrate while an agent worktree has uncommitted changes")
	}

	mergeCmd := exec.CommandContext(
		ctx,
		"git", "-C", austin.Path,
		"merge", "--no-edit", "--no-ff", tony.Branch,
	)
	var stdout, stderr bytes.Buffer
	mergeCmd.Stdout = &stdout
	mergeCmd.Stderr = &stderr
	err = mergeCmd.Run()
	message := strings.TrimSpace(stdout.String() + "\n" + stderr.String())
	if err != nil {
		conflicted := hasMergeConflicts(ctx, austin.Path)
		if conflicted {
			return IntegrationResult{
				AustinBranch: austin.Branch,
				AustinPath:   austin.Path,
				Head:         austin.Head,
				MergedTony:   tony.Head,
				Conflicted:   true,
				Message:      message,
			}, nil
		}
		return IntegrationResult{}, fmt.Errorf("merge Tony into Austin: %w\n%s", err, message)
	}

	head, err := gitOutput(ctx, austin.Path, "rev-parse", "HEAD")
	if err != nil {
		return IntegrationResult{}, err
	}
	return IntegrationResult{
		AustinBranch: austin.Branch,
		AustinPath:   austin.Path,
		Head:         strings.TrimSpace(head),
		MergedTony:   tony.Head,
		Conflicted:   false,
		Message:      message,
	}, nil
}

func (m *GitManager) Cleanup(ctx context.Context) error {
	var errs []string
	for _, wt := range []Worktree{m.set.Austin, m.set.Tony} {
		if wt.Path == "" {
			continue
		}
		cmd := exec.CommandContext(ctx, "git", "-C", m.set.Repository, "worktree", "remove", wt.Path)
		if output, err := cmd.CombinedOutput(); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v: %s", wt.Agent, err, strings.TrimSpace(string(output))))
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// FindRoot resolves a repository path to its Git top-level directory without
// touching the worktree. It is used before Duo decides fresh vs resume.
func FindRoot(ctx context.Context, repository string) (string, error) {
	repo := strings.TrimSpace(repository)
	if repo == "" {
		repo = "."
	}
	root, err := gitOutput(ctx, repo, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("Duo requires an existing Git repository: %w", err)
	}
	abs, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		return "", err
	}
	return abs, nil
}

// SamePath reports whether two paths denote the same location. Git reports
// resolved paths (on macOS /tmp is really /private/tmp), so a session recorded
// through one spelling must still validate against the other.
func SamePath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	resolvedA, errA := filepath.EvalSymlinks(a)
	resolvedB, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return false
	}
	return filepath.Clean(resolvedA) == filepath.Clean(resolvedB)
}

// Restore installs a previously persisted workspace set without touching Git or
// creating worktrees. Recovery validates it separately.
func (m *GitManager) Restore(set Set) { m.set = set }

// Head returns the current commit of an agent worktree.
func (m *GitManager) Head(ctx context.Context, agent protocol.AgentID) (string, error) {
	wt, ok := m.set.For(agent)
	if !ok {
		return "", fmt.Errorf("unknown agent: %s", agent)
	}
	head, err := gitOutput(ctx, wt.Path, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(head), nil
}

// MergeState describes an interrupted or completed merge in a worktree.
// During a conflicted merge, HEAD still points at the pre-merge commit and
// MERGE_HEAD holds the commit being merged in, which is what crash recovery
// needs to avoid duplicating the merge.
func (m *GitManager) MergeState(ctx context.Context, agent protocol.AgentID) (MergeState, error) {
	wt, ok := m.set.For(agent)
	if !ok {
		return MergeState{}, fmt.Errorf("unknown agent: %s", agent)
	}
	mergeHead, err := gitOutput(ctx, wt.Path, "rev-parse", "-q", "--verify", "MERGE_HEAD")
	mergeHead = strings.TrimSpace(mergeHead)
	if err != nil || mergeHead == "" {
		return MergeState{Conflicted: hasMergeConflicts(ctx, wt.Path)}, nil
	}
	return MergeState{
		InProgress: true,
		MergeHead:  mergeHead,
		Conflicted: hasMergeConflicts(ctx, wt.Path),
	}, nil
}

// IsAncestor reports whether ancestor is reachable from descendant.
func (m *GitManager) IsAncestor(ctx context.Context, agent protocol.AgentID, ancestor, descendant string) (bool, error) {
	wt, ok := m.set.For(agent)
	if !ok {
		return false, fmt.Errorf("unknown agent: %s", agent)
	}
	if strings.TrimSpace(ancestor) == "" || strings.TrimSpace(descendant) == "" {
		return false, nil
	}
	cmd := exec.CommandContext(ctx, "git", "-C", wt.Path, "merge-base", "--is-ancestor", ancestor, descendant)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git merge-base --is-ancestor: %w", err)
}

// Validate checks that an agent worktree exists and is the expected Git worktree
// on the expected branch. Dirty content is allowed and reported separately.
func (m *GitManager) Validate(ctx context.Context, agent protocol.AgentID) error {
	wt, ok := m.set.For(agent)
	if !ok {
		return fmt.Errorf("unknown agent: %s", agent)
	}
	if strings.TrimSpace(wt.Path) == "" {
		return fmt.Errorf("%s worktree path is not recorded in the session state", agent)
	}
	st, err := os.Stat(wt.Path)
	if err != nil || !st.IsDir() {
		return fmt.Errorf("%s worktree is missing at %s; restore it or start a new Duo session", agent, wt.Path)
	}
	actualRoot, err := gitOutput(ctx, wt.Path, "rev-parse", "--show-toplevel")
	if err != nil || !SamePath(strings.TrimSpace(actualRoot), wt.Path) {
		return fmt.Errorf("%s worktree at %s is not a Git worktree", agent, wt.Path)
	}
	if wt.Branch != "" {
		actualBranch, err := gitOutput(ctx, wt.Path, "branch", "--show-current")
		if err != nil {
			return err
		}
		if strings.TrimSpace(actualBranch) != wt.Branch {
			return fmt.Errorf("%s worktree is on branch %q, expected %q", agent, strings.TrimSpace(actualBranch), wt.Branch)
		}
	}
	return nil
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func hasMergeConflicts(ctx context.Context, dir string) bool {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "diff", "--name-only", "--diff-filter=U")
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

var invalidSession = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func sanitizeSession(value string) string {
	value = strings.TrimSpace(value)
	value = invalidSession.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-._")
	return value
}
