package sessionstore

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/atfa/duo/internal/protocol"
)

// writeLegacyState writes a literal state.json into a fresh store, so the
// fixture is exactly what a given Duo release produced rather than whatever the
// current Snapshot type happens to marshal to.
func writeLegacyState(t *testing.T, store *Store, state string) {
	t.Helper()
	if err := os.WriteFile(store.StatePath(), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
}

func stateSessionID(t *testing.T, state json.RawMessage) string {
	t.Helper()
	var ident Ident
	if err := json.Unmarshal(state, &ident); err != nil {
		t.Fatalf("decode driver state %q: %v", state, err)
	}
	return ident.SessionID
}

// TestLoadMigratesV090Snapshot pins the whole v0.9.0 migration against a
// literal fixture: one agent on agy, one on opencode, two identities from
// different drivers, no driverStates field in the file.
func TestLoadMigratesV090Snapshot(t *testing.T) {
	store := newTestStore(t, "session-1")
	writeLegacyState(t, store, `{
  "schemaVersion": 1,
  "duoVersion": "v0.9.0",
  "sessionId": "session-1",
  "repoId": "repo-1234",
  "repository": "/tmp/repo",
  "baseBranch": "main",
  "baseCommit": "abc123",
  "phase": "REVIEW",
  "plan": "the plan",
  "planVersion": 3,
  "started": true,
  "agentDrivers": {"Austin": "agy", "Tony": "opencode"},
  "piSessions": {
    "Austin": "11111111-1111-4111-8111-111111111111",
    "Tony": "ses_01ABCdef"
  },
  "ready": {}, "notes": {}, "evidence": {}, "worktrees": {},
  "integration": {}, "delivery": {},
  "createdAt": "2026-10-02T00:00:00Z",
  "updatedAt": "2026-10-02T00:00:00Z"
}`)

	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != SchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", got.SchemaVersion, SchemaVersion)
	}
	if len(got.DriverStates) != 2 {
		t.Fatalf("DriverStates = %+v, want both agents", got.DriverStates)
	}
	austin := got.DriverStates[protocol.Austin]
	if austin.Driver != "agy" || stateSessionID(t, austin.State) != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("Austin = %+v, want agy/11111111-1111-4111-8111-111111111111", austin)
	}
	tony := got.DriverStates[protocol.Tony]
	if tony.Driver != "opencode" || stateSessionID(t, tony.State) != "ses_01ABCdef" {
		t.Fatalf("Tony = %+v, want opencode/ses_01ABCdef", tony)
	}
	if got.PiSessions[protocol.Austin] != "11111111-1111-4111-8111-111111111111" ||
		got.PiSessions[protocol.Tony] != "ses_01ABCdef" {
		t.Fatalf("legacy piSessions mirror lost: %+v", got.PiSessions)
	}
}

func TestLoadMigratesMissingDriverToPi(t *testing.T) {
	store := newTestStore(t, "session-1")
	writeLegacyState(t, store, `{
  "schemaVersion": 1,
  "sessionId": "session-1",
  "piSessions": {"Austin": "aaa", "Tony": "bbb"},
  "ready": {}, "notes": {}, "evidence": {}, "worktrees": {},
  "integration": {}, "delivery": {}
}`)

	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		state, ok := got.DriverStates[agent]
		if !ok || state.Driver != "pi" {
			t.Fatalf("DriverStates[%s] = %+v, want driver pi", agent, state)
		}
	}
}

func TestMigratedDriverStatesRepairsSharedIdentity(t *testing.T) {
	snap := Snapshot{
		PiSessions: map[protocol.AgentID]string{
			protocol.Austin: "same-id",
			protocol.Tony:   "same-id",
		},
	}
	migrated := snap.MigratedDriverStates()
	if stateSessionID(t, migrated[protocol.Austin].State) != "same-id" {
		t.Fatalf("Austin should keep the shared id: %+v", migrated[protocol.Austin])
	}
	if _, ok := migrated[protocol.Tony]; ok {
		t.Fatalf("Tony must be left with nothing so the plugin mints a fresh id: %+v", migrated[protocol.Tony])
	}
}

func TestMigratedDriverStatesNeverReplacesLiveBlob(t *testing.T) {
	snap := Snapshot{
		AgentDrivers: map[protocol.AgentID]string{protocol.Austin: "agy"},
		PiSessions:   map[protocol.AgentID]string{protocol.Austin: "legacy-id"},
		DriverStates: map[protocol.AgentID]DriverState{
			protocol.Austin: {Driver: "agy", State: json.RawMessage(`{"sessionId":"live-id"}`)},
		},
	}
	migrated := snap.MigratedDriverStates()
	if stateSessionID(t, migrated[protocol.Austin].State) != "live-id" {
		t.Fatalf("live blob replaced by legacy value: %+v", migrated[protocol.Austin])
	}
}

// TestDriverStateRoundTrip preserves a blob from a driver Core has never heard
// of, including fields it does not understand.
func TestDriverStateRoundTrip(t *testing.T) {
	store := newTestStore(t, "session-1")
	want := testSnapshot("session-1")
	want.DriverStates = map[protocol.AgentID]DriverState{
		protocol.Tony: {Driver: "codex", State: json.RawMessage(`{"sessionId":"s-1","opaque":{"nested":[1,2,3]}}`)},
	}
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	state := got.DriverStates[protocol.Tony]
	// Save re-indents embedded JSON, so the blob is compared by content.
	var wantBlob, gotBlob map[string]any
	if err := json.Unmarshal([]byte(`{"sessionId":"s-1","opaque":{"nested":[1,2,3]}}`), &wantBlob); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(state.State, &gotBlob); err != nil {
		t.Fatalf("unknown driver blob not valid JSON after round trip: %v", err)
	}
	if state.Driver != "codex" || !reflect.DeepEqual(gotBlob, wantBlob) {
		t.Fatalf("unknown driver blob changed: driver=%q state=%s", state.Driver, state.State)
	}
	austin := got.DriverStates[protocol.Austin]
	if austin.Driver != "pi" ||
		stateSessionID(t, austin.State) != want.PiSessions[protocol.Austin] {
		t.Fatalf("legacy identity not filled alongside the live blob: %+v", austin)
	}
	if len(got.DriverStates) != 2 {
		t.Fatalf("DriverStates = %+v, want the live blob plus the migrated legacy id", got.DriverStates)
	}
}

// TestMigratedDriverStatesReadsBridgeEraBlob pins compatibility with the M1
// bridge, which stringifies the plugin's state into the legacy piSessions
// field. Both spellings must migrate to the same session id.
func TestMigratedDriverStatesReadsBridgeEraBlob(t *testing.T) {
	snap := Snapshot{
		AgentDrivers: map[protocol.AgentID]string{protocol.Austin: "agy"},
		PiSessions: map[protocol.AgentID]string{
			protocol.Austin: `{"sessionId":"bridge-id"}`,
			protocol.Tony:   "bare-id",
		},
	}
	migrated := snap.MigratedDriverStates()
	austin := migrated[protocol.Austin]
	if austin.Driver != "agy" || string(austin.State) != `{"sessionId":"bridge-id"}` {
		t.Fatalf("bridge-era blob not preserved: %+v", austin)
	}
	if stateSessionID(t, migrated[protocol.Tony].State) != "bare-id" {
		t.Fatalf("bare id not migrated: %+v", migrated[protocol.Tony])
	}
}
