package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/atfa/duo/internal/protocol"
)

type Worktree struct {
	Agent  protocol.AgentID
	Path   string
	Branch string
	Head   string
}

type Set struct {
	Repository string
	BaseBranch string
	BaseCommit string
	Session    string
	Root       string
	// ScopePath is the repository-relative default directory for agents.
	// The empty value is the legacy spelling of the repository root.
	ScopePath string
	Austin    Worktree
	Tony      Worktree
}

// AgentDir returns an agent's scoped working directory. It validates persisted
// scope state before joining it to a worktree.
func (s Set) AgentDir(agent protocol.AgentID) (string, bool, error) {
	wt, ok := s.For(agent)
	if !ok || strings.TrimSpace(wt.Path) == "" {
		return "", false, nil
	}
	dir, err := ScopedPath(wt.Path, s.ScopePath)
	return dir, true, err
}

// ScopedPath safely maps a repository-relative scope into a worktree.
func ScopedPath(worktreeRoot, scope string) (string, error) {
	root, err := filepath.Abs(strings.TrimSpace(worktreeRoot))
	if err != nil {
		return "", err
	}
	scope = strings.TrimSpace(scope)
	if scope == "" || scope == "." {
		return root, nil
	}
	if filepath.IsAbs(scope) {
		return "", fmt.Errorf("cannot use Duo scope %q: must be repository-relative", scope)
	}
	clean := filepath.Clean(scope)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("cannot use Duo scope %q: escapes the repository", scope)
	}
	path := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("cannot use Duo scope %q: escapes the repository", scope)
	}
	canonicalRoot, err := canonicalPath(root)
	if err != nil {
		return "", err
	}
	current := root
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		if _, err := os.Lstat(current); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return "", err
		}
		resolved, err := filepath.EvalSymlinks(current)
		if err != nil {
			return "", fmt.Errorf("cannot use Duo scope %q: resolve path: %w", scope, err)
		}
		resolved = filepath.Clean(resolved)
		inside, err := filepath.Rel(canonicalRoot, resolved)
		if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) || filepath.IsAbs(inside) {
			return "", fmt.Errorf("cannot use Duo scope %q: escapes the repository", scope)
		}
	}
	return path, nil
}

// ScopePath derives a safe, repository-relative scope from the launch target.
func ScopePath(repositoryRoot, launchDir string) (string, error) {
	repo, err := canonicalPath(repositoryRoot)
	if err != nil {
		return "", err
	}
	launch, err := canonicalPath(launchDir)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(repo, launch)
	if err != nil {
		return "", err
	}
	if rel == "." {
		return ".", nil
	}
	if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("launch directory %s is outside repository %s", launchDir, repositoryRoot)
	}
	return rel, nil
}

func canonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved), nil
	}
	if _, err := os.Stat(abs); err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func (s Set) For(agent protocol.AgentID) (Worktree, bool) {
	switch agent {
	case protocol.Austin:
		return s.Austin, true
	case protocol.Tony:
		return s.Tony, true
	default:
		return Worktree{}, false
	}
}

func (s Set) String() string {
	return fmt.Sprintf(
		"Workspace:\n- repo: %s\n- scope: %s\n- base: %s (%s)\n- session: %s\n- Austin: %s [%s]\n- Tony: %s [%s]",
		s.Repository,
		effectiveScope(s.ScopePath),
		s.BaseBranch,
		shortSHA(s.BaseCommit),
		s.Session,
		s.Austin.Path,
		s.Austin.Branch,
		s.Tony.Path,
		s.Tony.Branch,
	)
}

func effectiveScope(scope string) string {
	if strings.TrimSpace(scope) == "" {
		return "."
	}
	return scope
}

type Status struct {
	Agent  protocol.AgentID
	Path   string
	Branch string
	Head   string
	Dirty  bool
	Ahead  int
}

func (s Status) String() string {
	return fmt.Sprintf(
		"%s: branch=%s head=%s dirty=%t ahead=%d path=%s",
		s.Agent,
		s.Branch,
		shortSHA(s.Head),
		s.Dirty,
		s.Ahead,
		s.Path,
	)
}

type Artifact struct {
	Agent      protocol.AgentID
	Branch     string
	Commit     string
	BaseCommit string
	Ahead      int
}

func (a Artifact) String() string {
	return fmt.Sprintf("%s %s@%s (+%d)", a.Agent, a.Branch, shortSHA(a.Commit), a.Ahead)
}

type IntegrationResult struct {
	AustinBranch string
	AustinPath   string
	Head         string
	MergedTony   string
	Conflicted   bool
	Message      string
}

// MergeState reports an in-progress merge in an agent worktree.
type MergeState struct {
	InProgress bool
	MergeHead  string
	Conflicted bool
}

type Manager interface {
	Prepare(context.Context) (Set, error)
	Set() Set
	Status(context.Context, protocol.AgentID) (Status, error)
	Head(context.Context, protocol.AgentID) (string, error)
	CaptureArtifact(context.Context, protocol.AgentID) (Artifact, error)
	IntegrateTonyIntoAustin(context.Context) (IntegrationResult, error)
	Cleanup(context.Context) error
}

func shortSHA(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 10 {
		return value[:10]
	}
	return value
}
