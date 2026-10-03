package sessionstore

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
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
	// The duplicate check compares extracted sessionIds, so it holds even when
	// the two agents' legacy values are spelled differently (bridge blob vs bare).
	snap := Snapshot{
		PiSessions: map[protocol.AgentID]string{
			protocol.Austin: `{"sessionId":"same-id"}`,
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
// bridge, which stringifies each plugin's state verbatim into the legacy
// piSessions field. Only sessionId is part of the contract; every other field
// belongs to the driver and must survive byte-for-byte, including in blobs
// that carry no sessionId at all (agy's first run writes only logFile).
func TestMigratedDriverStatesReadsBridgeEraBlob(t *testing.T) {
	cases := []struct {
		name      string
		driver    string
		blob      string
		wantID    string // "" means no sessionId is expected
		wantEntry bool
	}{
		{name: "pi", driver: "pi", blob: `{"sessionId":"pi-uuid"}`, wantID: "pi-uuid", wantEntry: true},
		{name: "opencode", driver: "opencode", blob: `{"sessionId":"ses_01ABCdef"}`, wantID: "ses_01ABCdef", wantEntry: true},
		{name: "agy learned", driver: "agy", blob: `{"conversationId":"conv-1","logFile":"/tmp/duo-agy-austin.log"}`, wantEntry: true},
		{name: "agy first run", driver: "agy", blob: `{"logFile":"/tmp/duo-agy-austin.log"}`, wantEntry: true},
		{name: "empty object", driver: "opencode", blob: `{}`, wantEntry: true},
		// Any non-object was a valid v0.9.0 id and migrates as itself; only an
		// object-shaped value that fails to parse is corrupt and dropped.
		{name: "bare string is an id", driver: "agy", blob: `not-an-object`, wantID: "not-an-object", wantEntry: true},
		{name: "broken object dropped", driver: "agy", blob: `{"broken`, wantEntry: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap := Snapshot{
				AgentDrivers: map[protocol.AgentID]string{protocol.Austin: tc.driver},
				PiSessions:   map[protocol.AgentID]string{protocol.Austin: tc.blob},
			}
			migrated := snap.MigratedDriverStates()
			state, ok := migrated[protocol.Austin]
			if ok != tc.wantEntry {
				t.Fatalf("entry present=%v, want %v (%+v)", ok, tc.wantEntry, migrated)
			}
			if !tc.wantEntry {
				return
			}
			if state.Driver != tc.driver {
				t.Fatalf("driver = %q, want %q", state.Driver, tc.driver)
			}
			if strings.HasPrefix(tc.blob, "{") {
				// Object blobs are preserved verbatim, byte-for-byte.
				if got := strings.TrimSpace(string(state.State)); got != tc.blob {
					t.Fatalf("blob changed:\n got %s\nwant %s", got, tc.blob)
				}
			}
			if stateSessionID(t, state.State) != tc.wantID {
				t.Fatalf("sessionId = %q, want %q", stateSessionID(t, state.State), tc.wantID)
			}
		})
	}
}
