package xduo

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/session"
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
	Updated   time.Time `json:"updated"`
	PID       int       `json:"pid"`
	Running   bool      `json:"running"`
}

func Snapshot(v session.View, running bool) Status {
	a, t := v.Agent(protocol.Austin), v.Agent(protocol.Tony)
	return Status{v.SessionID, string(v.Phase()), v.Project.Started, string(v.Pause.State), Agent{a.Driver, a.Model}, Agent{t.Driver, t.Model}, time.Now().UTC(), os.Getpid(), running}
}
func WriteStatus(path string, s Status) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".status-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
