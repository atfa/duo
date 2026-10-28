package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/atfa/duo/internal/xduo"
)

func TestTaskState(t *testing.T) {
	now := time.Now()
	fresh := xduo.Status{Started: true, Running: true, Updated: now, Phase: "EXECUTE"}
	cases := []struct {
		name        string
		s           xduo.Status
		valid, live bool
		want        string
	}{
		{"running", fresh, true, true, "playing"},
		{"exited", fresh, true, false, "stop"},
		{"missing", fresh, false, true, "Unknown"},
		{"stale", xduo.Status{Running: true, Updated: now.Add(-time.Minute)}, true, true, "Unknown"},
		{"done", xduo.Status{Running: true, Updated: now, Phase: "DONE"}, true, true, "stop"},
		{"paused", xduo.Status{Running: true, Updated: now, Pause: "paused"}, true, true, "pause"},
		{"starting", xduo.Status{Running: true, Updated: now}, true, true, "Starting"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := taskState(c.s, c.valid, c.live, now); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestInstancesUseSeparateTmuxSessionsAndFiles(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	dir := t.TempDir()
	t.Setenv("TMUX_TMPDIR", dir)
	t.Setenv("TMUX", "")
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-server").Run() })
	repo := filepath.Join(dir, "project with spaces")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", out, err)
	}
	duo := filepath.Join(dir, "duo test")
	if err := os.WriteFile(duo, []byte("#!/bin/sh\nsleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	a, err := create(dir, duo, repo)
	if err != nil {
		t.Fatal(err)
	}
	b, err := create(dir, duo, repo)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID || a.Status == b.Status || a.Pairing == b.Pairing || !alive(a.Tmux) || !alive(b.Tmux) {
		t.Fatalf("instances not isolated: %+v %+v", a, b)
	}
	path := filepath.Join(dir, "instances.json")
	if err := save(path, registry{Items: []instance{a, b}}); err != nil {
		t.Fatal(err)
	}
	r, err := load(path)
	if err != nil || len(r.Items) != 2 || r.Items[0] != a {
		t.Fatalf("registry: %+v %v", r, err)
	}
}
