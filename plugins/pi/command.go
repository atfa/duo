package pi

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/atfa/duo/internal/driver"
)

// DriverVersion is this plugin's version. It is independent of the Duo binary
// version: a plugin is released with its agent, not with Core.
const DriverVersion = "1.0.0"

// lookPath is exec.LookPath, overridable so tests do not depend on what happens to
// be installed.
var lookPath = exec.LookPath

// newUUID returns an RFC 4122 version 4 UUID, used as a Pi session identity.
var newUUID = func() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// NewSessionID mints a Pi session identity. Pi adopts an id it has not seen, so
// generating one up front is what lets a resumed session reattach to the same
// conversation. A driver whose agent assigns ids itself must not do this: it
// declares ResumeServer and sends no id until the agent reports one.
func NewSessionID() string {
	id, err := newUUID()
	if err != nil {
		// A missing identity costs Pi a fresh conversation, which is
		// recoverable; failing the launch is not.
		return ""
	}
	return id
}

// DefaultModel returns the model Pi runs with when the operator chose none.
//
// Pi's own settings are asked first, because that is the choice the human already
// made; the hardcoded fallback exists only so a fresh install has something to
// run. Reading another agent's config here is exactly what this package exists to
// stop Core from doing.
func DefaultModel() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		path := filepath.Join(home, ".pi", "agent", "settings.json")
		if data, err := os.ReadFile(path); err == nil {
			var s struct {
				DefaultProvider string `json:"defaultProvider"`
				DefaultModel    string `json:"defaultModel"`
			}
			if json.Unmarshal(data, &s) == nil {
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
}

// ListCommand builds the model-listing invocation.
//
// Pi takes a flag rather than a subcommand, so its launch command is reused
// verbatim with `--list-models` appended. Reusing the whole command is what keeps
// a custom config directory working: the catalog must come from the same
// installation the agents run from.
func ListCommand() string { return Name + " --list-models" }

// ParseListModels reads the fixed-column table printed by `pi --list-models`:
// provider, model id, context, max-out, then the thinking and images flags. The id
// is everything between the provider and the last four columns, so a model id that
// contains a space is still reconstructed faithfully. The header and any warning
// lines are ignored because their last two columns are not yes/no.
func ParseListModels(output string) ([]driver.Model, error) {
	var list []driver.Model
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
		list = append(list, driver.Model{
			Provider: fields[0],
			ID:       strings.Join(fields[1:len(fields)-4], " "),
			Thinking: thinking == "yes",
			Images:   images == "yes",
		})
	}
	return list, nil
}
