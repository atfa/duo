package agy

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/atfa/duo/internal/driver"
)

// DriverVersion is declared in agy.go; these are agy's own facts about itself.

// DefaultModel is the model agy runs with when the operator chose none.
const DefaultModel = "gemini-3.8-flash-high"

// ListCommand builds the model-listing invocation.
//
// Agy takes a subcommand rather than a flag, and that subcommand accepts no flags
// of its own. Only the executable is reused: appending a subcommand to the launch
// command would be read as the positional project path ("agy --auto models" tries
// to open a directory named "models"), and carrying over agent flags such as
// --model makes the subcommand print its usage instead of a catalog. Reusing just
// the executable still honours a custom binary path.
func ListCommand() string { return Name + " models" }

// ParseModels reads the output of `agy models`, which prints a spinner-prefixed
// line per model with a human description. The flags are inferred rather than
// parsed, because agy does not print them: a thinking family or level in the name
// means the model can reason, and every agy model takes images.
func ParseModels(output string) ([]driver.Model, error) {
	var list []driver.Model
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
		lower := strings.ToLower(id)
		thinking := strings.Contains(lower, "thinking") ||
			strings.Contains(lower, "high") ||
			strings.Contains(lower, "medium")
		list = append(list, driver.Model{ID: id, Thinking: thinking, Images: true})
	}
	return list, nil
}

// appDataDir is where agy keeps its configuration and transcripts.
func appDataDir() string {
	if dir := os.Getenv("GEMINI_APP_DATA_DIR"); dir != "" {
		return dir
	}
	if dir := os.Getenv("ANTIGRAVITY_APP_DATA_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".gemini", "antigravity-cli")
}

// TranscriptPath is the transcript agy writes for a conversation, or "" when agy's
// location is unknown. The Agent Adapter follows this file to observe the agent.
func TranscriptPath(conversationID string) string {
	base := appDataDir()
	if base == "" || conversationID == "" {
		return ""
	}
	return filepath.Join(base, "brain", conversationID, ".system_generated", "logs", "transcript.jsonl")
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// waitForConversationID polls agy's log for the conversation it opened. agy only
// reports the id once it has one, so on a first run the adapter has to watch for
// it rather than being told.
func waitForConversationID(logFile string) string {
	if logFile == "" {
		return ""
	}
	for {
		if fileExists(logFile) {
			if f, err := os.Open(logFile); err == nil {
				id, _ := ExtractConversationID(bufio.NewReader(f))
				_ = f.Close()
				if id != "" {
					return id
				}
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
}
