package sessionstore

import (
	"encoding/json"
	"strings"

	"github.com/atfa/duo/internal/protocol"
)

// DriverState is one agent's opaque, plugin-owned resume blob. Duo Core stores
// it and hands it back without interpreting it, which is what lets a new driver
// ship resume support without a change to this schema.
type DriverState struct {
	Driver string          `json:"driver"`
	State  json.RawMessage `json:"state,omitempty"`
}

// Ident is the shape every driver must accept in its state, so a v0.9.0
// session migrates without the driver knowing anything about the migration.
type Ident struct {
	SessionID string `json:"sessionId"`
}

// MigratedDriverStates returns the authoritative per-agent driver state map for
// this snapshot. Snapshots written before driverStates existed carry only
// AgentDrivers and PiSessions; those are converted here so every reader sees
// DriverStates regardless of which Duo wrote the file. A blob already present
// always wins over a legacy value, so a re-migration can never regress state a
// plugin has since updated.
func (s Snapshot) MigratedDriverStates() map[protocol.AgentID]DriverState {
	agents := []protocol.AgentID{protocol.Austin, protocol.Tony}
	out := make(map[protocol.AgentID]DriverState, len(s.DriverStates)+len(s.PiSessions))
	for agent, state := range s.DriverStates {
		out[agent] = state
	}

	type legacyEntry struct {
		id  string
		raw json.RawMessage
	}
	legacy := make(map[protocol.AgentID]legacyEntry, len(agents))
	ids := make(map[protocol.AgentID]string, len(agents))
	for _, agent := range agents {
		if id, raw, ok := s.legacyIdentity(agent); ok {
			legacy[agent] = legacyEntry{id: id, raw: raw}
			ids[agent] = id
		}
	}
	shared := ids[protocol.Austin] != "" && ids[protocol.Austin] == ids[protocol.Tony]

	for _, agent := range agents {
		if _, ok := out[agent]; ok {
			continue // a blob the plugin itself wrote always wins over a legacy value
		}
		entry, ok := legacy[agent]
		if !ok {
			continue
		}
		if shared && agent != protocol.Austin {
			// Both agents recorded the same id. Replaying it twice would put them
			// in one conversation, so only the first agent keeps it and the second
			// is left with nothing — the plugin's cue to mint a fresh one.
			continue
		}
		driverName := s.AgentDrivers[agent]
		if driverName == "" {
			driverName = "pi"
		}
		out[agent] = DriverState{Driver: driverName, State: entry.raw}
	}
	return out
}

// legacyIdentity resolves an agent's pre-driverStates state, returning the
// session id when one can be read (used only for cross-agent duplicate
// detection), the raw state blob to persist, and whether there was a value at
// all. Two legacy spellings must both migrate:
//
//   - the bare id v0.9.0 wrote into piSessions, wrapped in the contract shape;
//   - the JSON blob the M1 bridge stringified into the same field, preserved
//     verbatim. Only sessionId is part of the contract; every other field —
//     agy's conversationId and logFile, for example — belongs to the driver and
//     must survive untouched, including when sessionId is absent entirely.
//
// A bare value that is not an object cannot be a corrupt blob and is migrated
// as the id it has always been; an object-looking value that is not valid JSON
// is dropped rather than migrated, so corruption can never become a live
// session identity.
func (s Snapshot) legacyIdentity(agent protocol.AgentID) (string, json.RawMessage, bool) {
	value := strings.TrimSpace(s.PiSessions[agent])
	if value == "" {
		return "", nil, false
	}
	if !strings.HasPrefix(value, "{") {
		raw, err := json.Marshal(Ident{SessionID: value})
		if err != nil {
			return "", nil, false
		}
		return value, raw, true
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(value), &probe); err != nil {
		return "", nil, false
	}
	var ident Ident
	id := ""
	if err := json.Unmarshal([]byte(value), &ident); err == nil {
		id = strings.TrimSpace(ident.SessionID)
	}
	return id, json.RawMessage(value), true
}
