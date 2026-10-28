package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/session"
)

type xduoStatusAgent struct {
	Driver string `json:"driver"`
	Model  string `json:"model"`
}
type xduoStatus struct {
	SessionID string          `json:"session_id"`
	Phase     string          `json:"phase"`
	Started   bool            `json:"started"`
	Pause     string          `json:"pause"`
	Austin    xduoStatusAgent `json:"austin"`
	Tony      xduoStatusAgent `json:"tony"`
	UpdatedAt time.Time       `json:"updated_at"`
	PID       int             `json:"pid"`
	Process   string          `json:"process"`
}

type statusPublisher struct {
	stop chan struct{}
	done chan struct{}
}

func startStatusPublisher(path string, view func() session.View) *statusPublisher {
	p := &statusPublisher{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(p.done)
		logged := false
		write := func(process string) {
			v := view()
			a, t := v.Agent(protocol.Austin), v.Agent(protocol.Tony)
			s := xduoStatus{SessionID: v.SessionID, Phase: string(v.Phase()), Started: v.Project.Started, Pause: string(v.Pause.State), Austin: xduoStatusAgent{a.Driver, a.Model}, Tony: xduoStatusAgent{t.Driver, t.Model}, UpdatedAt: time.Now().UTC(), PID: os.Getpid(), Process: process}
			if err := writeXduoStatus(path, s); err != nil && !logged {
				log.Printf("Duo status publication: %v", err)
				logged = true
			}
		}
		write("running")
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				write("running")
			case <-p.stop:
				write("exited")
				return
			}
		}
	}()
	return p
}
func (p *statusPublisher) Close() { close(p.stop); <-p.done }

func writeXduoStatus(path string, status xduoStatus) error {
	data, err := json.Marshal(status)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".status-*.json")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
