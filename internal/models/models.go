// Package models reads the model catalog from a Pi installation. Duo never
// keeps its own model list: `pi --list-models` is the single source of truth, so
// the picker always matches what the running Pi can actually select.
package models

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Model is one selectable Pi model. Provider and ID together identify it
// unambiguously, because a model ID may itself contain a provider prefix.
type Model struct {
	Provider string
	ID       string
	Thinking bool
	Images   bool
}

// Reference is the canonical provider/id form, used for display and filtering.
// For models with empty provider or agy driver, only ID is returned.
func (m Model) Reference() string {
	if m.Provider == "" || m.Provider == "agy" {
		return m.ID
	}
	return m.Provider + "/" + m.ID
}

// List runs the driver's own model-listing command and returns the catalog.
//
// How the listing is invoked depends on how the CLI spells it:
//
//   - pi takes a flag, so Duo's launch command is reused verbatim and
//     `--list-models` is appended, which keeps a custom config directory working.
//   - agy and opencode take a subcommand, and that subcommand accepts no flags of
//     its own. Two things break if the launch command is reused: a subcommand
//     appended at the end is read as the positional project path
//     (`opencode --auto models` tries to open a directory named "models"), and
//     carrying over agent flags such as `--model` makes the subcommand print its
//     usage instead of a catalog. So only the executable is reused, which still
//     honors a custom binary path.
func List(ctx context.Context, command string) ([]Model, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		command = "pi"
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	listCmd := listCommand(command)
	kind := driverKind(command)

	cmd := exec.CommandContext(ctx, "sh", "-lc", listCmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return nil, fmt.Errorf("list models: %s", detail)
	}

	var list []Model
	switch kind {
	case "agy":
		list = parseAgyModels(stdout.String())
	case "opencode":
		list = parseOpencodeModels(stdout.String())
	default:
		list = parse(stdout.String())
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("no models reported by %q", command)
	}
	return list, nil
}

// listCommand builds the model-listing invocation for a launch command.
func listCommand(command string) string {
	switch driverKind(command) {
	case "agy", "opencode":
		return executable(command) + " models"
	default:
		return command + " --list-models"
	}
}

// executable returns the command's first token, which is the program being run.
// Quoting is left untouched: the result is handed to a shell, so an operator's
// custom binary path keeps working.
func executable(command string) string {
	trimmed := strings.TrimSpace(command)
	if idx := strings.IndexAny(trimmed, " \t"); idx >= 0 {
		return trimmed[:idx]
	}
	return trimmed
}

// driverKind identifies which agent CLI a launch command refers to, so the
// model catalog is read with that CLI's own listing syntax.
func driverKind(command string) string {
	trimmed := strings.TrimSpace(command)
	switch {
	case strings.HasPrefix(trimmed, "agy") || strings.Contains(trimmed, "/agy"):
		return "agy"
	case strings.HasPrefix(trimmed, "opencode") || strings.Contains(trimmed, "opencode"):
		return "opencode"
	default:
		return "pi"
	}
}

// parseOpencodeModels reads `opencode models`, which prints one plain
// provider/model reference per line.
func parseOpencodeModels(output string) []Model {
	var list []Model
	seen := make(map[string]bool)
	output = strings.ReplaceAll(output, "\r", "\n")
	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		provider, id, ok := strings.Cut(line, "/")
		if !ok {
			continue
		}
		provider = strings.TrimSpace(provider)
		id = strings.TrimSpace(id)
		if provider == "" || id == "" {
			continue
		}
		key := provider + "/" + id
		if seen[key] {
			continue
		}
		seen[key] = true
		list = append(list, Model{Provider: provider, ID: id})
	}
	return list
}

func parseAgyModels(output string) []Model {
	var list []Model
	output = strings.ReplaceAll(output, "\r", "\n")
	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		if idx := strings.LastIndex(line, "Fetching available models..."); idx != -1 {
			line = strings.TrimSpace(line[idx+len("Fetching available models..."):])
		}
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		id := fields[0]
		if id == "NAME" || id == "MODEL" || id == "Available" {
			continue
		}
		thinking := strings.Contains(line, "Thinking") ||
			strings.Contains(id, "high") ||
			strings.Contains(id, "medium") ||
			strings.Contains(id, "thinking")
		list = append(list, Model{
			Provider: "",
			ID:       id,
			Thinking: thinking,
			Images:   true,
		})
	}
	return list
}

// parse reads the fixed-column table printed by `pi --list-models`: provider,
// model id, context, max-out, then the thinking and images flags. The id is
// everything between the provider and the last four columns, so a model id that
// contains a space is still reconstructed faithfully. The header and any
// warning lines are ignored because their last two columns are not yes/no.
func parse(output string) []Model {
	var list []Model
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		thinking, images := fields[len(fields)-2], fields[len(fields)-1]
		if thinking != "yes" && thinking != "no" {
			continue
		}
		if images != "yes" && images != "no" {
			continue
		}
		list = append(list, Model{
			Provider: fields[0],
			ID:       strings.Join(fields[1:len(fields)-4], " "),
			Thinking: thinking == "yes",
			Images:   images == "yes",
		})
	}
	return list
}

// DefaultModelForDriver returns a sensible default model for the given driver.
// For "agy", it returns "gemini-3.8-flash-high".
// For "pi" (or unspecified), it inspects ~/.pi/agent/settings.json if present,
// falling back to "anthropic/claude-sonnet-4-6".
// For "opencode" it returns the user's configured model, or "" to let opencode
// resolve its own default.
func DefaultModelForDriver(driverType string) string {
	switch strings.ToLower(strings.TrimSpace(driverType)) {
	case "agy":
		return "gemini-3.8-flash-high"
	case "opencode":
		// opencode resolves its own default model (global config, then its
		// built-in catalog). Injecting a foreign provider id here would fail
		// with "Model not found", so an unconfigured opencode gets no --model
		// and keeps opencode's own choice.
		return opencodeConfiguredModel()
	case "pi", "":
		if home, err := os.UserHomeDir(); err == nil {
			settingsPath := filepath.Join(home, ".pi", "agent", "settings.json")
			if data, err := os.ReadFile(settingsPath); err == nil {
				var s struct {
					DefaultProvider string `json:"defaultProvider"`
					DefaultModel    string `json:"defaultModel"`
				}
				if err := json.Unmarshal(data, &s); err == nil {
					provider := strings.TrimSpace(s.DefaultProvider)
					model := strings.TrimSpace(s.DefaultModel)
					if provider != "" && model != "" {
						if strings.HasPrefix(model, provider+"/") {
							return model
						}
						return provider + "/" + model
					}
					if model != "" {
						return model
					}
				}
			}
		}
		return "anthropic/claude-sonnet-4-6"
	default:
		return "anthropic/claude-sonnet-4-6"
	}
}

// opencodeConfiguredModel reads the model the user already configured for
// opencode, so Duo does not override their choice with a guess. opencode accepts
// either opencode.json or opencode.jsonc globally, so both are checked.
func opencodeConfiguredModel() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	configDir := filepath.Join(home, ".config", "opencode")
	var data []byte
	var readErr error
	for _, name := range []string{"opencode.json", "opencode.jsonc"} {
		data, readErr = os.ReadFile(filepath.Join(configDir, name))
		if readErr == nil {
			break
		}
	}
	if readErr != nil {
		return ""
	}
	// The config may be JSONC, so a comment must not fail the parse.
	if stripped := stripJSONComments(string(data)); stripped != "" {
		data = []byte(stripped)
	}
	var cfg struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return ""
	}
	return strings.TrimSpace(cfg.Model)
}

// stripJSONComments removes // and /* */ comments and trailing commas so a
// JSONC config can be parsed with encoding/json. String literals are preserved.
func stripJSONComments(src string) string {
	var out strings.Builder
	inString := false
	escaped := false
	for i := 0; i < len(src); i++ {
		c := src[i]
		if inString {
			out.WriteByte(c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out.WriteByte(c)
			continue
		}
		if c == '/' && i+1 < len(src) {
			switch src[i+1] {
			case '/':
				for i < len(src) && src[i] != '\n' {
					i++
				}
				out.WriteByte('\n')
				continue
			case '*':
				i += 2
				for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
					i++
				}
				i++
				continue
			}
		}
		out.WriteByte(c)
	}
	return strings.TrimSpace(out.String())
}
