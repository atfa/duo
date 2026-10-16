package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestUpgradeRequestRoundTrips is the handoff between the Duo that stops and the
// launcher that resumes: a missing or unreadable session id would leave a
// preserved session nobody can continue.
func TestUpgradeRequestRoundTrips(t *testing.T) {
	repo := t.TempDir()
	if err := writeUpgradeRequest(repo, "20261016-070818-abcd", "install then resume", "3/7", true); err != nil {
		t.Fatalf("writeUpgradeRequest: %v", err)
	}
	req, err := readUpgradeRequest(repo)
	if err != nil {
		t.Fatalf("readUpgradeRequest: %v", err)
	}
	if req.SessionID != "20261016-070818-abcd" || req.Step != "3/7" || !req.Install {
		t.Fatalf("round-trip lost fields: %+v", req)
	}
	if req.SchemaVersion == 0 || req.DuoVersion == "" {
		t.Fatalf("the request must record what wrote it: %+v", req)
	}

	// The file is the only handoff, so it has to be complete at every instant: a
	// half-written request would resume the wrong session.
	data, err := os.ReadFile(upgradeRequestPath(repo))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("the request must be valid JSON at rest: %v", err)
	}
	if _, err := os.Stat(upgradeRequestPath(repo) + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("the temporary file must not survive a completed write")
	}

	// A missing request is an error, never a silent resume of some other session.
	if _, err := readUpgradeRequest(t.TempDir()); err == nil {
		t.Fatal("an absent request must be an error")
	}
	if err := os.WriteFile(upgradeRequestPath(repo), []byte(`{"reason":"no session"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readUpgradeRequest(repo); err == nil {
		t.Fatal("a request without a session id must be an error")
	}
}

// TestCheckSchemaCompatibleRefusesABump is the loop's guard: installing a binary
// whose snapshot schema this session does not match would leave a session that
// neither binary can continue, so the loop must stop instead.
func TestCheckSchemaCompatibleRefusesABump(t *testing.T) {
	repo := t.TempDir()
	pkg := filepath.Join(repo, "internal", "sessionstore")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(pkg, "snapshot.go")

	// No source to inspect: the loop cannot judge, and it must not refuse to
	// resume a preserved session because of that.
	if err := checkSchemaCompatible(repo); err != nil {
		t.Fatalf("a missing source file must not block the loop: %v", err)
	}

	if err := os.WriteFile(path, []byte("const SchemaVersion = 99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkSchemaCompatible(repo); err == nil {
		t.Fatal("a schema bump must stop the loop before installing")
	}

	same := []byte("// SchemaVersion is the persisted snapshot schema.\nconst SchemaVersion = 2\n")
	if err := os.WriteFile(path, same, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkSchemaCompatible(repo); err != nil {
		t.Fatalf("a matching schema must be accepted: %v", err)
	}
}

func TestExitCodeOf(t *testing.T) {
	if got := exitCodeOf(nil); got != 0 {
		t.Fatalf("exitCodeOf(nil) = %d, want 0", got)
	}
	if got := exitCodeOf(os.ErrNotExist); got != -1 {
		t.Fatalf("a non-exit error = %d, want -1 so it is not mistaken for the upgrade code", got)
	}
}
