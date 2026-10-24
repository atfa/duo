package sessionstore

import (
	"errors"
	"os"
	"testing"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
)

// v2Snapshot is a snapshot exactly as the previous Duo wrote it: schema 2, the
// workflow mode and each step's policy left implicit, and the signatures and
// queue already recorded.
const v2Snapshot = `{
  "schemaVersion": 2,
  "duoVersion": "v0.14.0",
  "sessionId": "session-v2",
  "repoId": "repo-1234",
  "repository": "/tmp/repo",
  "baseBranch": "main",
  "baseCommit": "abc123",
  "phase": "INTEGRATE",
  "plan": "the plan",
  "planVersion": 2,
  "started": true,
  "mode": "step",
  "step": 2,
  "steps": [
    {"title": "one", "done": true, "head": "aaaa1111", "deliveryHead": "aaaa1111"},
    {"title": "two", "policy": "goal"}
  ],
  "ready": {"Austin": true, "Tony": true},
  "notes": {"Austin": "looks good"},
  "evidence": {"Austin": "deadbeef"},
  "worktrees": {},
  "integration": {"started": true},
  "createdAt": "2024-01-01T00:00:00Z",
  "updatedAt": "2024-01-01T00:00:00Z"
}`

func writeRawSnapshot(t *testing.T, store *Store, raw string) {
	t.Helper()
	if err := os.WriteFile(store.StatePath(), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestResumeMigratesV2ToV3 is the promise that an upgrade does not strand an
// unfinished session: a v2 snapshot loads through the migration chain, keeps the
// signatures and the queue it recorded, and is saved back as the current schema.
func TestResumeMigratesV2ToV3(t *testing.T) {
	store := newTestStore(t, "session-v2")
	writeRawSnapshot(t, store, v2Snapshot)

	got, err := store.Load()
	if err != nil {
		t.Fatalf("a v2 snapshot must migrate instead of being refused: %v", err)
	}
	if got.SchemaVersion != SchemaVersion {
		t.Fatalf("schemaVersion = %d, want %d", got.SchemaVersion, SchemaVersion)
	}
	if got.PersistedSchemaVersion != 2 {
		t.Fatalf("persistedSchemaVersion = %d, want 2", got.PersistedSchemaVersion)
	}
	if got.Phase != "INTEGRATE" || got.PlanVersion != 2 || got.Step != 2 {
		t.Fatalf("workflow state lost in migration: %+v", got)
	}
	if !got.Ready[protocol.Austin] || !got.Ready[protocol.Tony] {
		t.Fatalf("signatures lost in migration: %+v", got.Ready)
	}
	if got.Evidence[protocol.Austin] != "deadbeef" || got.Notes[protocol.Austin] != "looks good" {
		t.Fatalf("evidence or notes lost in migration: %+v %+v", got.Evidence, got.Notes)
	}
	if got.Mode != project.ModeStep {
		t.Fatalf("mode = %q, want step recorded explicitly", got.Mode)
	}
	if len(got.Steps) != 2 {
		t.Fatalf("steps lost in migration: %+v", got.Steps)
	}
	if !got.Steps[0].Done || got.Steps[0].Head != "aaaa1111" || got.Steps[0].DeliveryHead != "aaaa1111" {
		t.Fatalf("a landed step lost its record: %+v", got.Steps[0])
	}
	// v2 left the policy of an untagged step implicit; v3 writes it down so a
	// later default cannot reinterpret the queue.
	if got.Steps[0].Policy != project.ModeFast || got.Steps[1].Policy != project.ModeGoal {
		t.Fatalf("policies not recorded explicitly: %+v", got.Steps)
	}

	// Saving a migrated snapshot persists the current schema, and reloading it is
	// not a second migration.
	if err := store.Save(got); err != nil {
		t.Fatal(err)
	}
	again, err := store.Load()
	if err != nil {
		t.Fatalf("a migrated snapshot must reload: %v", err)
	}
	if again.PersistedSchemaVersion != SchemaVersion {
		t.Fatalf("persistedSchemaVersion after save = %d, want %d", again.PersistedSchemaVersion, SchemaVersion)
	}
	if len(again.Steps) != 2 || !again.Ready[protocol.Tony] || again.Steps[1].Policy != project.ModeGoal {
		t.Fatalf("a migrated snapshot changed on the second load: %+v", again)
	}
}

// TestResumeRejectsUnknownFutureVersion keeps the refusal for a schema this
// binary cannot reach: only a version with a complete migration chain is read.
func TestResumeRejectsUnknownFutureVersion(t *testing.T) {
	store := newTestStore(t, "session-future")
	future := `{"schemaVersion": 99, "sessionId": "session-future", "repoId": "repo-1234"}`
	writeRawSnapshot(t, store, future)

	var schemaErr *SchemaError
	if _, err := store.Load(); !errors.As(err, &schemaErr) {
		t.Fatalf("expected SchemaError for an unknown future schema, got %v", err)
	}
	if schemaErr.Found != 99 || schemaErr.Supported != SchemaVersion {
		t.Fatalf("SchemaError reported %+v", schemaErr)
	}

	if !SchemaCompatible(2) || !SchemaCompatible(SchemaVersion) {
		t.Fatal("the current schema and a version with a migration step must be compatible")
	}
	if SchemaCompatible(SchemaVersion+1) || SchemaCompatible(1) {
		t.Fatal("a version without a migration chain must be refused")
	}
}
