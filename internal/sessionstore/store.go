package sessionstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrNoState reports that a session directory has no persisted snapshot yet.
var ErrNoState = errors.New("no persisted Duo session state")

// SchemaError reports a snapshot written by an incompatible Duo version.
type SchemaError struct {
	Path      string
	Found     int
	Supported int
}

func (e *SchemaError) Error() string {
	return fmt.Sprintf(
		"session state %s uses schema version %d but this Duo supports version %d; refusing to resume",
		e.Path, e.Found, e.Supported,
	)
}

// Store owns one session directory: state.json, events.jsonl, duo.log and lock.
type Store struct {
	dir string
	mu  sync.Mutex
}

// DefaultBaseDir returns ~/.duo/sessions.
func DefaultBaseDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".duo", "sessions"), nil
}

func New(baseDir, repoID, sessionID string) (*Store, error) {
	if !validName(repoID) {
		return nil, fmt.Errorf("invalid repo id %q", repoID)
	}
	if !validName(sessionID) {
		return nil, fmt.Errorf("invalid session id %q", sessionID)
	}
	dir := filepath.Join(baseDir, repoID, sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create session dir: %w", err)
	}
	return &Store{dir: dir}, nil
}

// Open attaches to an existing session directory without creating it.
func Open(baseDir, repoID, sessionID string) (*Store, error) {
	if !validName(repoID) || !validName(sessionID) {
		return nil, fmt.Errorf("invalid session reference %q/%q", repoID, sessionID)
	}
	dir := filepath.Join(baseDir, repoID, sessionID)
	info, err := os.Stat(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("unknown Duo session %q for repo %q", sessionID, repoID)
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("session path %s is not a directory", dir)
	}
	return &Store{dir: dir}, nil
}

func (s *Store) Dir() string        { return s.dir }
func (s *Store) StatePath() string  { return filepath.Join(s.dir, "state.json") }
func (s *Store) LockPath() string   { return filepath.Join(s.dir, "lock") }
func (s *Store) EventsPath() string { return filepath.Join(s.dir, "events.jsonl") }
func (s *Store) LogPath() string    { return filepath.Join(s.dir, "duo.log") }

// Exists reports whether a snapshot has been written.
func (s *Store) Exists() bool {
	_, err := os.Stat(s.StatePath())
	return err == nil
}

// Load reads the last complete snapshot. A corrupt or schema-mismatched
// state.json is reported explicitly; a leftover state.json.tmp is ignored,
// because the atomic rename guarantees state.json is always complete.
func (s *Store) Load() (Snapshot, error) {
	data, err := os.ReadFile(s.StatePath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Snapshot{}, fmt.Errorf("%w in %s", ErrNoState, s.dir)
		}
		return Snapshot{}, fmt.Errorf("read session state: %w", err)
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return Snapshot{}, fmt.Errorf("session state %s is corrupt: %w", s.StatePath(), err)
	}
	if snap.SchemaVersion != SchemaVersion {
		return Snapshot{}, &SchemaError{Path: s.StatePath(), Found: snap.SchemaVersion, Supported: SchemaVersion}
	}
	if strings.TrimSpace(snap.SessionID) == "" {
		return Snapshot{}, fmt.Errorf("session state %s is missing sessionId", s.StatePath())
	}
	return snap, nil
}

// Save writes the snapshot atomically: a fully fsynced temp file is renamed over
// state.json, then the directory is fsynced where the platform supports it. A
// crash mid-write cannot damage the previous complete snapshot.
func (s *Store) Save(snap Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	snap.SchemaVersion = SchemaVersion
	if snap.UpdatedAt.IsZero() {
		snap.UpdatedAt = time.Now().UTC()
	} else {
		snap.UpdatedAt = snap.UpdatedAt.UTC()
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session state: %w", err)
	}
	data = append(data, '\n')
	return atomicWrite(s.StatePath(), data, 0o600)
}

func atomicWrite(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp := path + ".tmp"

	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("open temp state file: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write temp state file: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("fsync temp state file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close temp state file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace session state: %w", err)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// Summary describes one discovered session for `duo --resume`.
type Summary struct {
	SessionID string
	Dir       string
	Snapshot  Snapshot
	Err       error
}

// Unfinished reports whether the session's phase is valid and not DONE.
func (s Summary) Unfinished() bool {
	return s.Err == nil && s.Snapshot.Phase != "" && s.Snapshot.Phase != "DONE"
}

// List returns every session directory for a repository, newest first.
func List(baseDir, repoID string) ([]Summary, error) {
	root := filepath.Join(baseDir, repoID)
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []Summary
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		store := &Store{dir: filepath.Join(root, entry.Name())}
		snap, loadErr := store.Load()
		out = append(out, Summary{SessionID: entry.Name(), Dir: store.dir, Snapshot: snap, Err: loadErr})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Snapshot.UpdatedAt.After(out[j].Snapshot.UpdatedAt)
	})
	return out, nil
}
