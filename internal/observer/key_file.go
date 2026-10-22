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

// KeyFilePath returns the owner-only plaintext file used for a saved observer key.
// The file is not encrypted; callers should prefer environment variables where
// persistent plaintext storage is not acceptable.
func KeyFilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return filepath.Join(home, ".duo", keyFileName), nil
}

// KeyFileExists reports whether a saved key file exists.
func KeyFileExists() bool {
	path, err := KeyFilePath()
	if err != nil {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// SaveAPIKey atomically replaces the saved key. A new ~/.duo directory is
// owner-only; an existing directory is left untouched because it also contains
// session data.
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
		return "", fmt.Errorf("create observer key directory: %w", err)
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
