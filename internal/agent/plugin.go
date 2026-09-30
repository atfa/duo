package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// LookupPlugin searches for an external driver plugin executable by driver name.
// It checks ~/.duo/plugins/duo-driver-<name>, ~/.duo/plugins/duo-<name>,
// and then system $PATH for duo-driver-<name> and duo-<name>.
func LookupPlugin(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false
	}

	candidates := []string{
		"duo-driver-" + name,
		"duo-" + name,
	}

	// 1. ~/.duo/plugins/
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		pluginDir := filepath.Join(home, ".duo", "plugins")
		for _, c := range candidates {
			p := filepath.Join(pluginDir, c)
			if isExecutable(p) {
				return p, true
			}
		}
	}

	// 2. System PATH
	for _, c := range candidates {
		if p, err := exec.LookPath(c); err == nil {
			if isExecutable(p) {
				return p, true
			}
		}
	}

	return "", false
}

// ListDiscoveredPlugins returns names of all external driver plugins found in ~/.duo/plugins/ and $PATH.
func ListDiscoveredPlugins() []string {
	var plugins []string
	seen := make(map[string]bool)

	checkFile := func(name, fullPath string) {
		if !isExecutable(fullPath) {
			return
		}
		var drvName string
		switch {
		case strings.HasPrefix(name, "duo-driver-"):
			drvName = strings.TrimPrefix(name, "duo-driver-")
		case strings.HasPrefix(name, "duo-") && name != "duo-mcp-server":
			drvName = strings.TrimPrefix(name, "duo-")
		}
		if drvName != "" && !seen[drvName] && drvName != "pi" && drvName != "agy" {
			seen[drvName] = true
			plugins = append(plugins, drvName)
		}
	}

	// Check ~/.duo/plugins
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		pluginDir := filepath.Join(home, ".duo", "plugins")
		if entries, err := os.ReadDir(pluginDir); err == nil {
			for _, entry := range entries {
				if !entry.IsDir() {
					checkFile(entry.Name(), filepath.Join(pluginDir, entry.Name()))
				}
			}
		}
	}

	return plugins
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode()&0111 != 0
}

// NewExternalSession creates a driver session backed by an external plugin executable.
func NewExternalSession(cfg Config, pluginPath string) *Session {
	cfg.Command = pluginPath
	return NewSession(cfg)
}
