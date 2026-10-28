package gotui

import (
	"strings"
	"testing"

	"github.com/atfa/duo/internal/agent"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/session"
)

func TestShortcutSlashCommands(t *testing.T) {
	for _, target := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		t.Run(string(target), func(t *testing.T) {
			fake := newFakeSession()
			view := fake.view.Agents[target]
			view.Process = agent.ProcessExited
			fake.view.Agents[target] = view
			app := New(fake)
			if !app.runSlash("/"+strings.ToLower(string(target))) || !strings.Contains(app.status, "not running") {
				t.Fatalf("native command did not reach attach: %q", app.status)
			}
			if !app.runSlash("/restart " + strings.ToLower(string(target))) {
				t.Fatal("restart not handled")
			}
			if len(fake.commands) != 1 {
				t.Fatal(fake.commands)
			}
			restart, ok := fake.commands[0].(session.RestartAgent)
			if !ok || restart.Agent != target {
				t.Fatal(fake.commands)
			}
		})
	}
	for _, command := range []string{"/restart", "/restart other", "/restart austin extra"} {
		fake := newFakeSession()
		app := New(fake)
		app.runSlash(command)
		if len(fake.commands) != 0 || !app.statusErr {
			t.Fatalf("invalid restart ran: %q %+v", command, fake.commands)
		}
	}
}
