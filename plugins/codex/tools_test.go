package codex

import (
	"strings"
	"testing"

	"github.com/atfa/duo/internal/mcp"
)

// The launch command wires Duo's MCP server (mcp_servers.duo.args=["mcp-server"]),
// so every coordination tool is callable in the agent's session. The declaration
// must be exactly the set Core registers: naming more would make Core advertise a
// tool the agent cannot call, naming less would make Core cut a clause for a tool
// it can.
func TestManifestDeclaresExposedTools(t *testing.T) {
	man, err := (&Plugin{}).Describe()
	if err != nil {
		t.Fatal(err)
	}
	if !man.MCP {
		t.Fatal("the launch command wires Duo's MCP server, so mcp must be true")
	}
	if got, want := strings.Join(man.Capabilities.Tools, ","), strings.Join(mcp.ToolNames(), ","); got != want {
		t.Errorf("capabilities.tools = %s; the launch command exposes exactly %s", got, want)
	}
}
