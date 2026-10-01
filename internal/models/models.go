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

// List runs `pi --list-models` and returns the catalog. The command is the same
// one Duo launches its agents with, so flags such as a custom config directory
// are honored; `--list-models` is appended last.
func List(ctx context.Context, command string) ([]Model, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		command = "pi"
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	var listCmd string
	isAgy := strings.HasPrefix(command, "agy") || strings.Contains(command, "/agy")
	if isAgy {
		listCmd = command + " models"
	} else {
		listCmd = command + " --list-models"
	}

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
	if isAgy {
		list = parseAgyModels(stdout.String())
	} else {
		list = parse(stdout.String())
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("no models reported by %q", command)
	}
	return list, nil
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
func DefaultModelForDriver(driverType string) string {
	switch strings.ToLower(strings.TrimSpace(driverType)) {
	case "agy":
		return "gemini-3.8-flash-high"
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
