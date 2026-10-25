package sessionstore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestEventLogCloseDropsLateWrites pins the shutdown race a session's own
// directory removal hit: a bridge disconnect handled after the session closed
// reopened events.jsonl inside the directory being deleted, so removing the
// directory failed with "directory not empty". A closed journal stays closed.
func TestEventLogCloseDropsLateWrites(t *testing.T) {
	log := &EventLog{path: filepath.Join(t.TempDir(), "events.jsonl")}
	log.Record("session_start", nil)
	log.Close()

	// Stand in for the directory removal that Cleanup performs.
	if err := os.Remove(log.path); err != nil {
		t.Fatalf("remove journal: %v", err)
	}
	log.Record("bridge_disconnect", map[string]any{"agent": "Tony"})

	if _, err := os.Stat(log.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("closed journal was written again: %v", err)
	}
}
