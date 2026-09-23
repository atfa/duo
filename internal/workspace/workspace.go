package workspace

import (
	"context"
	"fmt"
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
	Austin     Worktree
	Tony       Worktree
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
		"Workspace:\n- repo: %s\n- base: %s (%s)\n- session: %s\n- Austin: %s [%s]\n- Tony: %s [%s]",
		s.Repository,
		s.BaseBranch,
		shortSHA(s.BaseCommit),
		s.Session,
		s.Austin.Path,
		s.Austin.Branch,
		s.Tony.Path,
		s.Tony.Branch,
	)
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

type Manager interface {
	Prepare(context.Context) (Set, error)
	Set() Set
	Status(context.Context, protocol.AgentID) (Status, error)
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
