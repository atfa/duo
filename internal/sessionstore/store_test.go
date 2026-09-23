package sessionstore

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/protocol"
)

func testSnapshot(sessionID string) Snapshot {
	return Snapshot{
		SessionID:   sessionID,
		RepoID:      "repo-1234",
		Repository:  "/tmp/repo",
		BaseBranch:  "main",
		BaseCommit:  "abc123",
		Phase:       "REVIEW",
		Plan:        "the plan",
		PlanVersion: 3,
		Started:     true,
		Ready:       map[protocol.AgentID]bool{protocol.Austin: true},
		Notes:       map[protocol.AgentID]string{protocol.Austin: "looks good"},
		Evidence:    map[protocol.AgentID]string{protocol.Austin: "deadbeef"},
		Worktrees: map[protocol.AgentID]Worktree{
			protocol.Austin: {Path: "/tmp/repo-austin", Branch: "duo/s/austin"},
			protocol.Tony:   {Path: "/tmp/repo-tony", Branch: "duo/s/tony"},
		},
		PiSessions: map[protocol.AgentID]string{
			protocol.Austin: "11111111-1111-4111-8111-111111111111",
			protocol.Tony:   "22222222-2222-4222-8222-222222222222",
		},
		CreatedAt: time.Now().UTC().Add(-time.Hour),
	}
}

func newTestStore(t *testing.T, sessionID string) *Store {
	t.Helper()
	store, err := New(t.TempDir(), "repo-1234", sessionID)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// TestSnapshotRoundTrip is the basic durability guarantee: everything survives
// a save/load cycle, including signatures and Pi identity.
func TestSnapshotRoundTrip(t *testing.T) {
	store := newTestStore(t, "session-1")
	want := testSnapshot("session-1")
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Phase != "REVIEW" || got.Plan != "the plan" || got.PlanVersion != 3 || !got.Started {
		t.Fatalf("domain state lost: %+v", got)
	}
	if !got.Ready[protocol.Austin] || got.Evidence[protocol.Austin] != "deadbeef" {
		t.Fatalf("signature state lost: %+v", got)
	}
	if got.PiSessions[protocol.Austin] != want.PiSessions[protocol.Austin] ||
		got.PiSessions[protocol.Tony] != want.PiSessions[protocol.Tony] {
		t.Fatalf("pi session identity lost: %+v", got.PiSessions)
	}
	if got.Worktrees[protocol.Tony].Branch != "duo/s/tony" {
		t.Fatalf("worktrees lost: %+v", got.Worktrees)
	}
	if got.SchemaVersion != SchemaVersion || got.UpdatedAt.IsZero() {
		t.Fatalf("version/timestamp not maintained: %+v", got)
	}
}

// TestSaveIsAtomicAndLeavesNoTempFile proves the atomic rename completes and
// that a stale temp file is never treated as state.
func TestSaveIsAtomicAndLeavesNoTempFile(t *testing.T) {
	store := newTestStore(t, "session-2")
	if err := store.Save(testSnapshot("session-2")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.StatePath() + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp file left behind: %v", err)
	}

	// A leftover temp file with garbage must not affect loading.
	if err := os.WriteFile(store.StatePath()+".tmp", []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	snap, err := store.Load()
	if err != nil {
		t.Fatalf("leftover temp file broke load: %v", err)
	}
	if snap.Plan != "the plan" {
		t.Fatalf("loaded unexpected snapshot: %+v", snap)
	}
}

func TestLoadReportsMissingAndCorruptState(t *testing.T) {
	store := newTestStore(t, "session-3")

	if _, err := store.Load(); !errors.Is(err, ErrNoState) {
		t.Fatalf("missing state should report ErrNoState, got %v", err)
	}

	if err := os.WriteFile(store.StatePath(), []byte("{ this is not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := store.Load()
	if err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("corrupt state must be reported explicitly, got %v", err)
	}
}

// TestLoadRejectsUnsupportedSchemaVersion makes a silent reset to PLAN
// impossible when an older Duo reads a newer checkpoint.
func TestLoadRejectsUnsupportedSchemaVersion(t *testing.T) {
	store := newTestStore(t, "session-4")
	snap := testSnapshot("session-4")
	snap.SchemaVersion = SchemaVersion + 99
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.StatePath(), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	var schemaErr *SchemaError
	if _, err := store.Load(); !errors.As(err, &schemaErr) {
		t.Fatalf("expected SchemaError, got %v", err)
	}
	if !strings.Contains(schemaErr.Error(), "refusing to resume") {
		t.Fatalf("schema error must refuse explicitly: %s", schemaErr)
	}
}

func TestListSkipsNothingAndReportsBrokenSessions(t *testing.T) {
	base := t.TempDir()
	good, err := New(base, "repo-1234", "good")
	if err != nil {
		t.Fatal(err)
	}
	if err := good.Save(testSnapshot("good")); err != nil {
		t.Fatal(err)
	}

	broken, err := New(base, "repo-1234", "broken")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(broken.StatePath(), []byte("{ nope"), 0o600); err != nil {
		t.Fatal(err)
	}

	items, err := List(base, "repo-1234")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 entries, got %d: %+v", len(items), items)
	}
	var ok, failed int
	for _, item := range items {
		if item.Err != nil {
			failed++
		} else {
			ok++
		}
	}
	if ok != 1 || failed != 1 {
		t.Fatalf("expected one healthy and one broken session, got ok=%d failed=%d", ok, failed)
	}
}

// TestLockIsExclusiveAndRecoversAfterCrash covers requirement 11: a second Duo
// must be refused, and a crashed owner must not leave a blocking stale lock.
func TestLockIsExclusiveAndRecoversAfterCrash(t *testing.T) {
	store := newTestStore(t, "session-5")

	first, err := store.Lock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Lock(); err == nil {
		t.Fatal("second lock must be refused while the first is held")
	} else {
		var lockedErr *LockedError
		if !errors.As(err, &lockedErr) {
			t.Fatalf("expected LockedError, got %v", err)
		}
		if !strings.Contains(lockedErr.Error(), "already active") {
			t.Fatalf("unhelpful lock error: %s", lockedErr)
		}
	}

	// A crash means the process disappears; Release models the OS dropping the
	// flock, after which the stale lock file must not block a resume.
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	second, err := store.Lock()
	if err != nil {
		t.Fatalf("stale lock file blocked resume: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}

	// Release is idempotent and nil-safe.
	if err := second.Release(); err != nil {
		t.Fatalf("second release should be a no-op, got %v", err)
	}
	var nilLock *Lock
	if err := nilLock.Release(); err != nil {
		t.Fatalf("nil release should be a no-op, got %v", err)
	}
}

// TestLockRecordsHolderMetadata checks the diagnostic metadata is written so a
// refusal can name the owning process.
func TestLockRecordsHolderMetadata(t *testing.T) {
	store := newTestStore(t, "session-6")
	lock, err := store.Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()

	data, err := os.ReadFile(store.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 3 {
		t.Fatalf("lock metadata incomplete: %q", data)
	}
	if holder := readHolder(store.LockPath()); holder.PID != os.Getpid() || holder.Hostname == "" {
		t.Fatalf("holder metadata not readable: %+v", holder)
	}
}

func TestEventLogAppendsJSONLines(t *testing.T) {
	store := newTestStore(t, "session-7")
	log := store.OpenEvents()

	log.Record("plan_updated", map[string]any{"planVersion": 2})
	log.Record("signature", map[string]any{"agent": "Austin", "phase": "REVIEW"})

	data, err := os.ReadFile(store.EventsPath())
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 event lines, got %d: %q", len(lines), data)
	}
	var event Event
	if err := json.Unmarshal([]byte(lines[1]), &event); err != nil {
		t.Fatalf("event line is not valid JSON: %v", err)
	}
	if event.Type != "signature" || event.Fields["agent"] != "Austin" || event.Time.IsZero() {
		t.Fatalf("unexpected event: %+v", event)
	}
}

// TestLoggerRedactsSecrets covers requirement 13: tokens must never reach the
// runtime log, even if they are formatted into a message.
func TestLoggerRedactsSecrets(t *testing.T) {
	store := newTestStore(t, "session-8")
	log := store.OpenLog()
	log.AddSecret("super-secret-token")
	log.Printf("bridge token is %s and again %s", "super-secret-token", "super-secret-token")
	log.AddSecret("") // ignored, must not panic or mask everything

	data, err := os.ReadFile(store.LogPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "super-secret-token") {
		t.Fatalf("secret leaked into log:\n%s", data)
	}
	if strings.Count(string(data), "[redacted]") != 2 {
		t.Fatalf("expected both occurrences redacted:\n%s", data)
	}
}

func TestRepoIDIsStableAndPathScoped(t *testing.T) {
	a := RepoID("/tmp/project-a")
	if a != RepoID("/tmp/project-a") {
		t.Fatal("RepoID must be stable for the same path")
	}
	if a == RepoID("/tmp/project-b") {
		t.Fatal("RepoID must differ for different paths")
	}
	if strings.ContainsAny(a, `/\`) || strings.HasPrefix(a, ".") {
		t.Fatalf("RepoID must be a safe single path element: %q", a)
	}
}

func TestStoreRejectsUnsafeSessionIDs(t *testing.T) {
	base := t.TempDir()
	for _, id := range []string{"..", "../escape", "a/b", `a\b`, ""} {
		if _, err := New(base, "repo-1234", id); err == nil {
			t.Fatalf("session id %q must be rejected", id)
		}
		if _, err := Open(base, "repo-1234", id); err == nil {
			t.Fatalf("open with session id %q must be rejected", id)
		}
	}
}

func TestOpenUnknownSessionReportsClearly(t *testing.T) {
	_, err := Open(t.TempDir(), "repo-1234", "missing-session")
	if err == nil || !strings.Contains(err.Error(), "unknown Duo session") {
		t.Fatalf("expected a clear unknown-session error, got %v", err)
	}
}

func TestNewUUIDIsUniqueAndWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 32; i++ {
		id, err := NewUUID()
		if err != nil {
			t.Fatal(err)
		}
		if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[14] != '4' {
			t.Fatalf("malformed uuid: %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate uuid: %q", id)
		}
		seen[id] = true
	}
}
