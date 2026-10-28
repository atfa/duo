package xduo

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRegistryRestoreAndIdentity(t *testing.T) {
	dir := t.TempDir()
	a, err := NewInstance(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewInstance(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID || a.StatusFile == b.StatusFile || filepath.Base(a.PairingFile) != "pairing.json" {
		t.Fatal("instances not isolated")
	}
	path := filepath.Join(dir, "instances.json")
	if got, err := LoadRegistry(path); err != nil || len(got) != 0 {
		t.Fatalf("missing: %v %v", got, err)
	}
	if err := SaveRegistry(path, []Instance{a, b}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || bytes.Contains(data, []byte("last_activated_at")) {
		t.Fatalf("unexpected activation timestamp: %s, %v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("permissions: %v %v", info, err)
	}
	got, err := LoadRegistry(path)
	if err != nil || len(got) != 2 || got[0] != a {
		t.Fatalf("restore: %+v %v", got, err)
	}
	if err := SaveRegistry(path, []Instance{a, a}); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRegistry(path); err == nil {
		t.Fatal("duplicate id accepted")
	}
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRegistry(path); err == nil {
		t.Fatal("corrupt registry accepted")
	}
}

func TestInspectSeparatesWorkflowLivenessAndFreshness(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	i := Instance{Session: "test", StatusFile: filepath.Join(t.TempDir(), "status.json")}
	alive := true
	probe := func(session string) (bool, error) {
		if session != i.Session {
			t.Fatal(session)
		}
		return alive, nil
	}
	check := func(want string, fresh bool) DisplayStatus {
		t.Helper()
		d, err := Inspect(i, now, probe)
		if err != nil || d.State != want || d.Fresh != fresh {
			t.Fatalf("got %+v %v; want %s fresh=%v", d, err, want, fresh)
		}
		return d
	}
	check("Starting", false)
	s := Status{SessionID: "s", Phase: "EXECUTE", Started: true, Pause: "running", Process: "running", UpdatedAt: now}
	write := func() {
		t.Helper()
		data, _ := json.Marshal(s)
		if err := os.WriteFile(i.StatusFile, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	check("playing", true)
	s.Pause = "paused"
	write()
	check("pause", true)
	s.Phase = "DONE"
	write()
	d := check("stop", true)
	if !d.Alive || d.Snapshot.Phase != "DONE" {
		t.Fatal(d)
	}
	s.Phase = "EXECUTE"
	s.UpdatedAt = now.Add(-6 * time.Second)
	write()
	check("Unknown", false)
	alive = false
	d = check("stop", false)
	if d.Alive || d.Snapshot.Phase != "EXECUTE" {
		t.Fatal(d)
	}
}
