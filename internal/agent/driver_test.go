package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/atfa/duo/internal/protocol"
)

func TestDriverInterfaceAndPiAgyCommandLines(t *testing.T) {
	// 1. Pi driver session
	piCfg := Config{
		Agent:       protocol.Austin,
		DriverType:  "pi",
		Dir:         t.TempDir(),
		Command:     "pi",
		Session:     "sess-123",
		PiSessionID: "pi-austin-1",
	}
	piSession := NewPiSession(piCfg)

	// Verify Driver interface implementation
	var driver Driver = piSession
	if driver.Agent() != protocol.Austin {
		t.Fatalf("driver.Agent() = %v, want Austin", driver.Agent())
	}
	if driver.DriverType() != "pi" {
		t.Fatalf("driver.DriverType() = %q, want \"pi\"", driver.DriverType())
	}
	if driver.SessionID() != "pi-austin-1" {
		t.Fatalf("driver.SessionID() = %q, want \"pi-austin-1\"", driver.SessionID())
	}
	if cmd := driver.EffectiveCommand(); !strings.Contains(cmd, `pi --session-id "$DUO_PI_SESSION_ID"`) {
		t.Fatalf("pi commandLine = %q, want --session-id", cmd)
	}

	// 2. Agy driver session
	agyCfg := Config{
		Agent:             protocol.Tony,
		DriverType:        "agy",
		Dir:               t.TempDir(),
		Command:           "agy",
		Session:           "sess-123",
		AgyConversationID: "agy-tony-conv",
	}
	agySession := NewAgySession(agyCfg)

	var agyDriver Driver = agySession
	if agyDriver.Agent() != protocol.Tony {
		t.Fatalf("agyDriver.Agent() = %v, want Tony", agyDriver.Agent())
	}
	if agyDriver.DriverType() != "agy" {
		t.Fatalf("agyDriver.DriverType() = %q, want \"agy\"", agyDriver.DriverType())
	}
	if agyDriver.SessionID() != "agy-tony-conv" {
		t.Fatalf("agyDriver.SessionID() = %q, want \"agy-tony-conv\"", agyDriver.SessionID())
	}
	if cmd := agyDriver.EffectiveCommand(); !strings.Contains(cmd, `agy --conversation "$DUO_AGY_CONVERSATION_ID" --dangerously-skip-permissions`) {
		t.Fatalf("agy commandLine = %q, want --conversation and --dangerously-skip-permissions", cmd)
	}

	// 3. Manager holding heterogeneous drivers
	m := NewManager()
	var events []LifecycleEvent
	m.SetObserver(func(e LifecycleEvent) {
		events = append(events, e)
	})

	m.Add(driver)
	m.Add(agyDriver)

	d1, ok1 := m.Driver(protocol.Austin)
	if !ok1 || d1.DriverType() != "pi" {
		t.Fatalf("m.Driver(Austin) = %v, %v, want pi driver", d1, ok1)
	}
	d2, ok2 := m.Session(protocol.Tony) // tests backward-compatible Session() method
	if !ok2 || d2.DriverType() != "agy" {
		t.Fatalf("m.Session(Tony) = %v, %v, want agy driver", d2, ok2)
	}
	if cmd := m.Command(); cmd != "pi" {
		t.Fatalf("m.Command() = %q, want \"pi\"", cmd)
	}
}

func TestDriverLifecycleAndExitObserver(t *testing.T) {
	ctx := context.Background()
	m := NewManager()
	var mu sync.Mutex
	var events []LifecycleEvent
	m.SetObserver(func(e LifecycleEvent) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	})

	austin := NewSession(Config{
		Agent:   protocol.Austin,
		Dir:     t.TempDir(),
		Command: "sh -c 'exit 0'",
	})
	tony := NewAgySession(Config{
		Agent:   protocol.Tony,
		Dir:     t.TempDir(),
		Command: "sh -c 'exit 0'",
	})

	m.Add(austin)
	m.Add(tony)

	if err := m.StartAll(ctx); err != nil {
		t.Fatal(err)
	}

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		var exitCount int
		for _, e := range events {
			if e.Kind == "agent_exit" {
				exitCount++
			}
		}
		return exitCount == 2
	})

	mu.Lock()
	defer mu.Unlock()
	var startCount, exitCount int
	for _, e := range events {
		switch e.Kind {
		case "agent_start":
			startCount++
		case "agent_exit":
			exitCount++
		}
	}
	if startCount != 2 {
		t.Fatalf("expected 2 agent_start events, got %d", startCount)
	}
	if exitCount != 2 {
		t.Fatalf("expected 2 agent_exit events, got %d", exitCount)
	}
}
