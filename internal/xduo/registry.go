// Package xduo provides the persistent state used by the xduo manager.
package xduo

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Instance struct {
	ID              string    `json:"id"`
	Project         string    `json:"project_path"`
	Session         string    `json:"tmux_session"`
	StatusFile      string    `json:"status_file"`
	PairingFile     string    `json:"pairing_file"`
	CreatedAt       time.Time `json:"created_at"`
	LastActivatedAt time.Time `json:"last_activated_at,omitempty"`
}

func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if home == "" {
		return "", errors.New("xduo: no home directory")
	}
	return filepath.Join(home, ".duo", "xduo"), nil
}

// NewInstance allocates an owner-only directory and stable, unique identity.
// Project must already have been normalized and validated by the caller.
func NewInstance(dir, project string) (Instance, error) {
	if !filepath.IsAbs(project) {
		return Instance{}, errors.New("xduo: project path must be absolute")
	}
	if err := os.MkdirAll(filepath.Join(dir, "instances"), 0700); err != nil {
		return Instance{}, err
	}
	for {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return Instance{}, err
		}
		id := hex.EncodeToString(random[:])
		instanceDir := filepath.Join(dir, "instances", id)
		if err := os.Mkdir(instanceDir, 0700); err != nil {
			if os.IsExist(err) {
				continue
			}
			return Instance{}, err
		}
		return Instance{ID: id, Project: project, Session: "xduo-" + id, StatusFile: filepath.Join(instanceDir, "status.json"), PairingFile: filepath.Join(instanceDir, "pairing.json"), CreatedAt: time.Now().UTC()}, nil
	}
}

// LoadRegistry restores records, pruning malformed entries and rejecting ambiguous identities.
// Missing tmux sessions are retained: they represent stopped instances.
func LoadRegistry(path string) ([]Instance, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var records []Instance
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("xduo: parse registry: %w", err)
	}
	seenID, seenSession := map[string]bool{}, map[string]bool{}
	valid := make([]Instance, 0, len(records))
	for _, i := range records {
		if i.ID == "" || seenID[i.ID] {
			return nil, fmt.Errorf("xduo: empty or duplicate instance id %q", i.ID)
		}
		seenID[i.ID] = true
		if i.Session == "" || !filepath.IsAbs(i.Project) || !filepath.IsAbs(i.StatusFile) || !filepath.IsAbs(i.PairingFile) || i.CreatedAt.IsZero() || seenSession[i.Session] {
			continue
		}
		seenSession[i.Session] = true
		valid = append(valid, i)
	}
	return valid, nil
}

func SaveRegistry(path string, records []Instance) error {
	if records == nil {
		records = []Instance{}
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".instances-*.json")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
