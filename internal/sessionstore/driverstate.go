package sessionstore

import (
	"encoding/json"
)

// DriverState is one agent's opaque, plugin-owned resume blob. Duo Core stores
// it and hands it back without interpreting it.
type DriverState struct {
	Driver string          `json:"driver"`
	State  json.RawMessage `json:"state,omitempty"`
}
