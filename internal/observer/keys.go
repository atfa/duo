package observer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	keyFileName = "observer.key"
	keyFileMode = 0o600
	keyDirMode  = 0o700
)

// KeyFilePath names the owner-only file a key saved from Duo's own interface
// lives in.
//
// It is plain text protected by file permissions, not an encrypted keychain:
// anything running as the same user can read it. An operator who does not want a
// file at all can use DUO_OBSERVER_API_KEY or the variable APIKeyEnv names
// instead; both take precedence over this file.
func KeyFilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return filepath.Join(home, ".duo", keyFileName), nil
}

// KeyFileExists reports whether a saved key exists, without reading it.
func KeyFileExists() bool {
	path, err := KeyFilePath()
	if err != nil {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// SaveAPIKey stores key for later sessions, replacing any previous value
// atomically.
//
// The file is created 0600. Directories are created 0700 when missing; an
// existing ~/.duo keeps its permissions, because Duo shares that directory with
// session records.
func SaveAPIKey(key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", errors.New("observer: empty API key")
	}
	path, err := KeyFilePath()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, keyDirMode); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, keyFileName+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("create observer key: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(keyFileMode); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("protect observer key: %w", err)
	}
	if _, err := tmp.WriteString(key + "\n"); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write observer key: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("flush observer key: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close observer key: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", fmt.Errorf("install observer key: %w", err)
	}
	return path, nil
}

// readKeyFile returns the saved key, or empty when there is none.
func readKeyFile() string {
	path, err := KeyFilePath()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
