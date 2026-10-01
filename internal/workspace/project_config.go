package workspace

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/atfa/duo/internal/protocol"
)

// ProjectConfigFile represents the structure of .duo/config.json in a repository.
type ProjectConfigFile struct {
	Driver string                      `json:"driver,omitempty"`
	Agents map[string]ProjectAgentFile `json:"agents,omitempty"`
}

// ProjectAgentFile describes one agent's configuration in .duo/config.json.
type ProjectAgentFile struct {
	Driver string `json:"driver,omitempty"`
	Model  string `json:"model,omitempty"`
}

// EnsureGitIgnore excludes .duo/ from Git using .git/info/exclude only.
//
// Duo deliberately does not write to the user's .gitignore. That file is
// tracked, so adding a line dirties the repository the user is working in and
// makes "is my repo clean?" depend on Duo having run. .git/info/exclude is
// local, untracked, and shared by every worktree of the repository, so it hides
// .duo/ everywhere without touching anything the user can see in a commit.
func EnsureGitIgnore(repoRoot string) error {
	repoRoot = strings.TrimSpace(repoRoot)
	if repoRoot == "" {
		return nil
	}
	ensureGitExclude(repoRoot)
	return nil
}

func appendLineIfMissing(path string, entry string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			content := entry
			if !strings.HasSuffix(content, "\n") {
				content += "\n"
			}
			return os.WriteFile(path, []byte(content), 0644)
		}
		return err
	}

	norm := strings.Trim(strings.TrimSpace(entry), "/")
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.Trim(strings.TrimSpace(scanner.Text()), "/")
		if line == norm || line == norm+"/**" {
			return nil
		}
	}

	var buf bytes.Buffer
	buf.Write(data)
	if len(data) > 0 && data[len(data)-1] != '\n' {
		buf.WriteByte('\n')
	}
	buf.WriteString(entry)
	if !strings.HasSuffix(entry, "\n") {
		buf.WriteByte('\n')
	}
	return os.WriteFile(path, buf.Bytes(), 0644)
}

func ensureGitExclude(repoRoot string) {
	gitDir := filepath.Join(repoRoot, ".git")
	st, err := os.Stat(gitDir)
	if err != nil {
		return
	}
	var infoDir string
	if st.IsDir() {
		infoDir = filepath.Join(gitDir, "info")
	} else {
		data, err := os.ReadFile(gitDir)
		if err == nil && strings.HasPrefix(string(data), "gitdir:") {
			target := strings.TrimSpace(strings.TrimPrefix(string(data), "gitdir:"))
			if !filepath.IsAbs(target) {
				target = filepath.Join(repoRoot, target)
			}
			infoDir = filepath.Join(target, "info")
		}
	}
	if infoDir == "" {
		return
	}
	_ = os.MkdirAll(infoDir, 0755)
	_ = appendLineIfMissing(filepath.Join(infoDir, "exclude"), ".duo/")
}

// SaveProjectConfig writes or updates .duo/config.json in repoRoot with the active driver and models.
func SaveProjectConfig(repoRoot string, driver string, agentDrivers map[protocol.AgentID]string, agentModels map[protocol.AgentID]string) error {
	repoRoot = strings.TrimSpace(repoRoot)
	if repoRoot == "" {
		return nil
	}
	duoDir := filepath.Join(repoRoot, ".duo")
	if err := os.MkdirAll(duoDir, 0755); err != nil {
		return err
	}
	_ = EnsureGitIgnore(repoRoot)

	configPath := filepath.Join(duoDir, "config.json")
	var cfg ProjectConfigFile
	if data, err := os.ReadFile(configPath); err == nil {
		_ = json.Unmarshal(data, &cfg)
	}

	if cfg.Agents == nil {
		cfg.Agents = make(map[string]ProjectAgentFile)
	}

	for id, drv := range agentDrivers {
		key := strings.ToLower(string(id))
		entry := cfg.Agents[key]
		if drv != "" {
			entry.Driver = drv
		}
		cfg.Agents[key] = entry
	}

	// If individual agents have drivers configured, omit top-level driver to eliminate ambiguity.
	hasAgentDriver := false
	for _, a := range cfg.Agents {
		if a.Driver != "" {
			hasAgentDriver = true
			break
		}
	}
	if hasAgentDriver {
		cfg.Driver = ""
	} else if driver != "" {
		cfg.Driver = driver
	}

	for id, model := range agentModels {
		key := strings.ToLower(string(id))
		entry := cfg.Agents[key]
		if model != "" {
			drv := entry.Driver
			if drv == "" {
				drv = agentDrivers[id]
			}
			if drv == "" {
				drv = cfg.Driver
			}
			if drv == "" {
				drv = driver
			}
			if drv == "agy" && strings.Contains(model, "/") {
				model = model[strings.LastIndex(model, "/")+1:]
			}
			entry.Model = model
		}
		cfg.Agents[key] = entry
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmpFile := configPath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmpFile, configPath)
}
