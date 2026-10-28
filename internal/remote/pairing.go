package remote

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// PairingFileName is where the live pairing lives, inside the same ~/.duo
// directory as the other owner-only state. Keeping it means a session that
// restarts — including the fresh session `/new` starts — can resume the same
// room, so the phone does not have to scan a new code.
const PairingFileName = "remote.json"

// PairingPath returns the configured pairing file or ~/.duo/remote.json.
func PairingPath() (string, error) {
	if path := os.Getenv("DUO_REMOTE_PAIRING_FILE"); path != "" {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if home == "" {
		return "", errors.New("remote: no home directory")
	}
	return filepath.Join(home, ".duo", PairingFileName), nil
}

// LoadPairing reads the stored pairing. A missing file is not an error: it means
// this machine has never paired, and the session stays local and silent.
func LoadPairing(path string) (Pairing, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Pairing{}, false, nil
		}
		return Pairing{}, false, err
	}
	var p Pairing
	if err := json.Unmarshal(data, &p); err != nil {
		return Pairing{}, false, fmt.Errorf("remote: parse %s: %w", path, err)
	}
	if _, err := p.Secret(); err != nil {
		return Pairing{}, false, err
	}
	if !ValidRoom(p.Room) {
		return Pairing{}, false, fmt.Errorf("remote: %s holds a malformed room id", path)
	}
	return p, true, nil
}

// SavePairing writes the pairing owner-only. The secret is a bearer credential
// for this room, so the file is 0600 and replaced atomically: a crash must never
// leave a half-written pairing that a later session trusts.
func SavePairing(path string, p Pairing) error {
	if _, err := p.Secret(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".remote-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// DeletePairing removes the stored pairing. A missing file is not an error: the
// caller's intent is "this machine is not paired", and it already is not.
func DeletePairing(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// LivePairing loads the stored pairing and reports whether it may be resumed.
func LivePairing(path string, now time.Time) (Pairing, bool) {
	p, ok, err := LoadPairing(path)
	if err != nil || !ok {
		return Pairing{}, false
	}
	if !p.Live(now) {
		return Pairing{}, false
	}
	return p, true
}
