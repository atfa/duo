package sessionstore

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/atfa/duo/internal/protocol"
)

// SchemaVersion is the persisted snapshot schema. Loading a snapshot written by
// an incompatible schema is refused explicitly instead of resetting to PLAN.
const SchemaVersion = 1

type Worktree struct {
	Path   string `json:"path"`
	Branch string `json:"branch"`
}

type Integration struct {
	Started    bool   `json:"started"`
	Conflicted bool   `json:"conflicted"`
	Head       string `json:"head"`
	MergedTony string `json:"mergedTony"`
}

// Snapshot is the durable checkpoint for one Duo session. It combines the
// project domain state with workspace, Pi session identity and integration
// state. Git remains the ground truth for artifacts and evidence.
type Snapshot struct {
	SchemaVersion int    `json:"schemaVersion"`
	DuoVersion    string `json:"duoVersion"`

	SessionID  string `json:"sessionId"`
	RepoID     string `json:"repoId"`
	Repository string `json:"repository"`
	BaseBranch string `json:"baseBranch"`
	BaseCommit string `json:"baseCommit"`

	Phase       string `json:"phase"`
	Plan        string `json:"plan"`
	PlanVersion int    `json:"planVersion"`
	Started     bool   `json:"started"`

	Ready    map[protocol.AgentID]bool   `json:"ready"`
	Notes    map[protocol.AgentID]string `json:"notes"`
	Evidence map[protocol.AgentID]string `json:"evidence"`

	Worktrees  map[protocol.AgentID]Worktree `json:"worktrees"`
	PiSessions map[protocol.AgentID]string   `json:"piSessions"`

	Integration Integration `json:"integration"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (s Snapshot) Worktree(agent protocol.AgentID) (Worktree, bool) {
	wt, ok := s.Worktrees[agent]
	return wt, ok
}

// RepoID derives a stable, collision-resistant store name from the absolute
// repository path, so ~/Projects/foo and /tmp/foo never share a session store.
func RepoID(repository string) string {
	path := strings.TrimSpace(repository)
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	path = filepath.Clean(path)
	sum := sha256.Sum256([]byte(path))
	name := sanitize(filepath.Base(path))
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = "repo"
	}
	return fmt.Sprintf("%s-%s", name, hex.EncodeToString(sum[:4]))
}

// NewUUID returns a random RFC 4122 version 4 UUID, used for stable Pi session
// identities.
func NewUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

var invalidName = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// validName reports whether a store path component is safe to join: a single
// plain path element with no separators, no traversal and no hidden dot-only
// name. Session ids and repo ids both flow into filesystem paths.
func validName(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	if strings.ContainsAny(value, `/\`) || strings.ContainsRune(value, 0) {
		return false
	}
	return value == sanitize(value)
}

func sanitize(value string) string {
	value = strings.TrimSpace(value)
	value = invalidName.ReplaceAllString(value, "-")
	return strings.Trim(value, "-._")
}
