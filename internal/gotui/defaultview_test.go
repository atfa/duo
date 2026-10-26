package gotui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/atfa/duo/internal/workspace"
)

// The launch path applies the configured default view through SetDefaultView
// after New. A repository that never saved a view opens on that default; a
// repository whose human toggled F2 keeps their own choice; and a default is
// never written into a repository the default did not come from.
func TestSetDefaultViewAppliesWhenProjectSavedNothing(t *testing.T) {
	repo := t.TempDir()
	fake := newFakeSession()
	fake.view.Worktrees.Repository = repo

	app := New(fake)
	if app.viewSaved {
		t.Fatal("a fresh repository must not look like a saved preference")
	}
	app.SetDefaultView("split")
	if app.viewMode != viewSplit {
		t.Fatalf("default view split with no project preference: view = %v, want split", app.viewMode)
	}
}

func TestSetDefaultViewKeepsProjectPreference(t *testing.T) {
	repo := t.TempDir()
	if err := workspace.SaveProjectTUIView(repo, "split"); err != nil {
		t.Fatal(err)
	}
	fake := newFakeSession()
	fake.view.Worktrees.Repository = repo

	app := New(fake)
	if !app.viewSaved {
		t.Fatal("a saved project preference must be recognised")
	}
	app.SetDefaultView("timeline")
	if app.viewMode != viewSplit {
		t.Fatalf("project preference split vs default timeline: view = %v, want split", app.viewMode)
	}
}

func TestSetDefaultViewDoesNotWriteTheRepository(t *testing.T) {
	repo := t.TempDir()
	fake := newFakeSession()
	fake.view.Worktrees.Repository = repo

	app := New(fake)
	app.SetDefaultView("split")
	if _, err := os.Stat(filepath.Join(repo, ".duo", "config.json")); !os.IsNotExist(err) {
		t.Fatalf("a configured default must not become a project preference: %v", err)
	}
}

func TestSetDefaultViewIgnoresUnknownValues(t *testing.T) {
	repo := t.TempDir()
	fake := newFakeSession()
	fake.view.Worktrees.Repository = repo

	app := New(fake)
	app.SetDefaultView("board")
	if app.viewMode != viewTimeline {
		t.Fatalf("unknown default view: view = %v, want timeline", app.viewMode)
	}
	// Trimming and case do not make a value unknown.
	app.SetDefaultView("  Split ")
	if app.viewMode != viewSplit {
		t.Fatalf("padded default view: view = %v, want split", app.viewMode)
	}
}

func TestSetDefaultViewEmptyIsANoOp(t *testing.T) {
	repo := t.TempDir()
	fake := newFakeSession()
	fake.view.Worktrees.Repository = repo

	app := New(fake)
	app.SetDefaultView("")
	if app.viewMode != viewTimeline {
		t.Fatalf("empty default view: view = %v, want timeline", app.viewMode)
	}
}
