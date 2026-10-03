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

	ids := make(map[protocol.AgentID]string, len(agents))
	raws := make(map[protocol.AgentID]json.RawMessage, len(agents))
	for _, agent := range agents {
		if id, raw, ok := s.legacyIdentity(agent); ok {
			ids[agent] = id
			raws[agent] = raw
		}
	}
	shared := ids[protocol.Austin] != "" && ids[protocol.Austin] == ids[protocol.Tony]

	for _, agent := range agents {
		if _, ok := out[agent]; ok {
			continue // a blob the plugin itself wrote always wins over a legacy value
		}
		id, raw := ids[agent], raws[agent]
		if id == "" {
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
		out[agent] = DriverState{Driver: driverName, State: raw}
	}
	return out
}

// legacyIdentity resolves an agent's pre-driverStates identity, returning the
// session id (used for cross-agent duplicate detection) and the raw state blob
// to persist. Two legacy spellings must both migrate: the bare id v0.9.0 wrote
// into piSessions, and the JSON blob the M1 bridge stringified into the same
// field before DriverStates existed. A value that is neither is dropped rather
// than migrated, so a corrupt id can never become a live session identity.
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
	var ident Ident
	if err := json.Unmarshal([]byte(value), &ident); err != nil || strings.TrimSpace(ident.SessionID) == "" {
		return "", nil, false
	}
	// Preserve the blob as written: it may carry fields beyond sessionId that
	// belong to the driver.
	return ident.SessionID, json.RawMessage(value), true
}
