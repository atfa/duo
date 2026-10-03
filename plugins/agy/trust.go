package agy

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Sqlite3MissingNotice is shown when agy's token-usage probe cannot run.
const Sqlite3MissingNotice = "sqlite3 is required to inspect agy token usage; context metrics will be unavailable. Install sqlite3 via your package manager (e.g. brew install sqlite3 or apt install sqlite3)."

// EnsureWorkspaceTrusted adds the given directories to agy's trustedWorkspaces in
// settings.json so the interactive trust prompt is bypassed for unattended agent
// turns.
//
// This edits agy's own configuration, which is why it belongs to this plugin
// rather than to Duo Core. A different coding agent will have its own equivalent,
// and Core must not grow a switch for each of them.
func EnsureWorkspaceTrusted(paths ...string) error {
	dir := appDataDir()
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	settingsPath := filepath.Join(dir, "settings.json")

	settings := make(map[string]any)
	if data, err := os.ReadFile(settingsPath); err == nil {
		_ = json.Unmarshal(data, &settings)
	}

	var trusted []string
	if raw, ok := settings["trustedWorkspaces"].([]any); ok {
		for _, item := range raw {
			if s, ok := item.(string); ok && s != "" {
				trusted = append(trusted, s)
			}
		}
	} else if raw, ok := settings["trustedWorkspaces"].([]string); ok {
		trusted = append(trusted, raw...)
	}

	seen := make(map[string]bool)
	for _, t := range trusted {
		seen[filepath.Clean(t)] = true
	}

	changed := false
	for _, p := range paths {
		if p == "" {
			continue
		}
		clean := filepath.Clean(p)
		if !seen[clean] {
			trusted = append(trusted, clean)
			seen[clean] = true
			changed = true
		}
		// A worktree may be reached through a symlink; agy compares the resolved
		// path, so both spellings have to be trusted.
		if real, err := filepath.EvalSymlinks(clean); err == nil && real != clean {
			realClean := filepath.Clean(real)
			if !seen[realClean] {
				trusted = append(trusted, realClean)
				seen[realClean] = true
				changed = true
			}
		}
	}

	if !changed {
		return nil
	}

	settings["trustedWorkspaces"] = trusted
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp := settingsPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, settingsPath)
}
