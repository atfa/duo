package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/session"
)

func TestStatusPublisherFinalSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance", "status.json")
	v := session.View{SessionID: "session", Agents: map[protocol.AgentID]session.AgentView{protocol.Austin: {Driver: "pi", Model: "model"}}}
	p := startStatusPublisher(path, func() session.View { return v })
	p.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got xduoStatus
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.SessionID != "session" || got.Austin.Driver != "pi" || got.Austin.Model != "model" || got.Process != "exited" || got.PID != os.Getpid() || got.UpdatedAt.IsZero() {
		t.Fatalf("snapshot: %+v", got)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files: %v, %v", entries, err)
	}
}

func TestStatusWritesAreAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	if err := writeXduoStatus(path, xduoStatus{SessionID: "initial"}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 200; i++ {
			if err := writeXduoStatus(path, xduoStatus{SessionID: "replacement"}); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			return
		default:
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var status xduoStatus
			if err := json.Unmarshal(data, &status); err != nil || (status.SessionID != "initial" && status.SessionID != "replacement") {
				t.Fatalf("partial status %q: %v", data, err)
			}
		}
	}
}
