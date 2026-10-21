package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/workspace"
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

// TestUpgradeRequestPersistsBuildIdentity pins the field names the launcher
// reads: the request must carry the commit the old binary was built from, and
// whether that checkout had uncommitted changes.
func TestUpgradeRequestPersistsBuildIdentity(t *testing.T) {
	data, err := json.Marshal(upgradeRequest{DuoVersion: "v0.12.2", BuiltCommit: "abcdef123456", BuiltModified: true})
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["builtCommit"] != "abcdef123456" {
		t.Fatalf("builtCommit = %v, want the build revision", raw["builtCommit"])
	}
	if raw["builtModified"] != true {
		t.Fatalf("builtModified = %v, want true", raw["builtModified"])
	}
}

// TestStepLoopLaunchFindsTheGitRootAndKeepsTheLaunchDir is the regression for
// the broken self-upgrade chain: the session writes .duo/upgrade.json at the Git
// root, while the loop used to read it from its own working directory. A launch
// from a subdirectory or with an explicit repository argument therefore exited
// with 77 and then failed to find the session it was told to resume. The launch
// directory must survive a resume: it selects the config file (language and test
// command) the session was started with.
func TestStepLoopLaunchFindsTheGitRootAndKeepsTheLaunchDir(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	deliveryGit(t, root, "init", "-q")
	sub := filepath.Join(root, "internal", "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	check := func(name, gotRepo, gotLaunch, wantLaunch string) {
		t.Helper()
		if !workspace.SamePath(gotRepo, root) {
			t.Fatalf("%s: repository = %q, want %q", name, gotRepo, root)
		}
		if !workspace.SamePath(gotLaunch, wantLaunch) {
			t.Fatalf("%s: launch directory = %q, want %q", name, gotLaunch, wantLaunch)
		}
	}

	t.Setenv("DUO_REPO", "")
	t.Chdir(sub)
	gotRepo, gotLaunch, err := stepLoopLaunch(ctx, nil)
	if err != nil {
		t.Fatalf("resolve from a subdirectory: %v", err)
	}
	check("subdirectory", gotRepo, gotLaunch, sub)

	// A launch from outside the repository uses the explicit path unchanged,
	// including when it names a subdirectory rather than the root.
	elsewhere := t.TempDir()
	t.Chdir(elsewhere)
	gotRepo, gotLaunch, err = stepLoopLaunch(ctx, []string{sub, "--mode", "step"})
	if err != nil {
		t.Fatalf("resolve an explicit repository: %v", err)
	}
	check("explicit subdirectory", gotRepo, gotLaunch, sub)

	gotRepo, gotLaunch, err = stepLoopLaunch(ctx, []string{root})
	if err != nil {
		t.Fatalf("resolve an explicit root: %v", err)
	}
	check("explicit root", gotRepo, gotLaunch, root)

	// `resume <session-id>` has no repository positional: the id must not be
	// passed to Git, and DUO_REPO still decides which repository is resumed.
	t.Setenv("DUO_REPO", root)
	gotRepo, gotLaunch, err = stepLoopLaunch(ctx, []string{"resume", "20261021-153633-c361221a"})
	if err != nil {
		t.Fatalf("resolve a resume launch: %v", err)
	}
	check("resume from DUO_REPO", gotRepo, gotLaunch, root)

	// The same resume launched from a subdirectory keeps that directory, so the
	// config it selected at startup is the config it reads again.
	t.Setenv("DUO_REPO", "")
	t.Chdir(sub)
	gotRepo, gotLaunch, err = stepLoopLaunch(ctx, []string{"resume", "20261021-153633-c361221a"})
	if err != nil {
		t.Fatalf("resolve a resume from a subdirectory: %v", err)
	}
	check("resume from a subdirectory", gotRepo, gotLaunch, sub)

	t.Chdir(elsewhere)
	if _, _, err := stepLoopLaunch(ctx, nil); err == nil {
		t.Fatal("a launch outside a Git repository must fail once a request exists")
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

// TestPerformUpgradeChecksSchemaBeforeInstalling is the order the loop depends on,
// and the defect it closes: the guard used to run after the install, so a schema
// bump replaced the working binary first and only then refused to resume — leaving
// a session neither binary could continue. Each piece passed its own unit test;
// only the sequence was wrong.
func TestPerformUpgradeChecksSchemaBeforeInstalling(t *testing.T) {
	req := upgradeRequest{Repository: t.TempDir(), SessionID: "s-1", Install: true}

	t.Run("check then install", func(t *testing.T) {
		var calls []string
		check := func(string) error { calls = append(calls, "check"); return nil }
		install := func(context.Context, string) error { calls = append(calls, "install"); return nil }
		if err := performUpgrade(context.Background(), req, check, install); err != nil {
			t.Fatalf("performUpgrade: %v", err)
		}
		if got := strings.Join(calls, ","); got != "check,install" {
			t.Fatalf("action order = %q, want check,install", got)
		}
	})

	t.Run("a schema bump installs nothing", func(t *testing.T) {
		var calls []string
		check := func(string) error { calls = append(calls, "check"); return errors.New("schema 3 vs 2") }
		install := func(context.Context, string) error { calls = append(calls, "install"); return nil }
		if err := performUpgrade(context.Background(), req, check, install); err == nil {
			t.Fatal("a schema bump must stop the upgrade")
		}
		if got := strings.Join(calls, ","); got != "check" {
			t.Fatalf("actions on a schema bump = %q, want check only: nothing may be installed", got)
		}
	})

	t.Run("a plain restart neither checks nor installs", func(t *testing.T) {
		// With install=false the binary that resumes is the one already running, so a
		// change in the delivered source must not block a restart.
		var calls []string
		check := func(string) error { calls = append(calls, "check"); return errors.New("unreachable") }
		install := func(context.Context, string) error { calls = append(calls, "install"); return nil }
		if err := performUpgrade(context.Background(), upgradeRequest{Repository: req.Repository}, check, install); err != nil {
			t.Fatalf("a plain restart must not be refused: %v", err)
		}
		if len(calls) != 0 {
			t.Fatalf("a plain restart performed %v, want nothing", calls)
		}
	})
}
