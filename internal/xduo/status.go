package xduo

import (
	"encoding/json"
	"errors"
	"os"
	"time"
)

const (
	StatePlaying  = "playing"
	StateIdle     = "idle"
	StatePause    = "pause"
	StateStop     = "stop"
	StateUnknown  = "Unknown"
	StateStarting = "Starting"
)

type Agent struct {
	Driver string `json:"driver"`
	Model  string `json:"model"`
}

type Status struct {
	SessionID string    `json:"session_id"`
	Phase     string    `json:"phase"`
	Started   bool      `json:"started"`
	Pause     string    `json:"pause"`
	Austin    Agent     `json:"austin"`
	Tony      Agent     `json:"tony"`
	UpdatedAt time.Time `json:"updated_at"`
	PID       int       `json:"pid"`
	Process   string    `json:"process"`
}

// ReadStatus distinguishes a missing snapshot from a corrupt or unreadable one.
func ReadStatus(path string) (Status, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Status{}, false, nil
	}
	if err != nil {
		return Status{}, false, err
	}
	var s Status
	if err := json.Unmarshal(data, &s); err != nil {
		return Status{}, false, err
	}
	return s, true, nil
}

// DisplayStatus keeps tmux liveness independent from the last workflow snapshot.
type DisplayStatus struct {
	State      string
	Fresh      bool
	Alive      bool
	AliveKnown bool
	Snapshot   Status
}

// Classify combines the snapshot with an independently observed tmux session.
// A live session without a snapshot is Starting.
func Classify(s Status, present, alive, aliveKnown bool, now time.Time) DisplayStatus {
	d := DisplayStatus{State: StateUnknown, Alive: alive, AliveKnown: aliveKnown, Snapshot: s}
	if present && !s.UpdatedAt.IsZero() && !s.UpdatedAt.After(now.Add(time.Second)) && now.Sub(s.UpdatedAt) <= 5*time.Second {
		d.Fresh = true
	}
	if aliveKnown && !alive {
		d.State = StateStop
		return d
	}
	if !present && aliveKnown && alive {
		d.State = StateStarting
		return d
	}
	if !d.Fresh || !aliveKnown {
		return d
	}
	if s.Process == "exited" || s.Phase == "DONE" {
		d.State = StateStop
		return d
	}
	if s.Process != "running" {
		return d
	}
	if s.Pause == "paused" || s.Pause == "pausing" {
		d.State = StatePause
		return d
	}
	if s.Pause == "running" {
		if s.Started {
			d.State = StatePlaying
		} else {
			d.State = StateIdle
		}
	}
	return d
}

// Inspect reads a snapshot and obtains liveness from an injectable session probe.
// Probe errors leave liveness unknown while preserving the snapshot.
func Inspect(i Instance, now time.Time, probe func(string) (bool, error)) (DisplayStatus, error) {
	s, present, readErr := ReadStatus(i.StatusFile)
	alive, probeErr := probe(i.Session)
	d := Classify(s, present, alive, probeErr == nil, now)
	if readErr != nil && (!d.AliveKnown || d.Alive) {
		d.State = StateUnknown
	}
	return d, errors.Join(readErr, probeErr)
}
