package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/atfa/duo/internal/protocol"
)

func TestPluginDiscoveryAndExternalSession(t *testing.T) {
	tmpDir := t.TempDir()

	// Create fake plugin: duo-driver-mock
	pluginPath := filepath.Join(tmpDir, "duo-driver-mock")
	script := "#!/bin/sh\necho \"mock driver running\"\nexit 0\n"
	if err := os.WriteFile(pluginPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	// Add tmpDir to PATH so LookupPlugin finds it
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", tmpDir+":"+oldPath)

	foundPath, ok := LookupPlugin("mock")
	if !ok {
		t.Fatalf("expected LookupPlugin(\"mock\") to find plugin in PATH")
	}
	if foundPath != pluginPath {
		t.Fatalf("LookupPlugin found %q, want %q", foundPath, pluginPath)
	}

	discovered := ListDiscoveredPlugins()
	foundInDiscovered := false
	for _, p := range discovered {
		if p == "mock" {
			foundInDiscovered = true
			break
		}
	}
	if !foundInDiscovered {
		t.Fatalf("expected ListDiscoveredPlugins() to contain \"mock\", got %#v", discovered)
	}

	// Negative case
	if _, ok := LookupPlugin("non_existent_plugin_xyz"); ok {
		t.Fatalf("expected non_existent_plugin_xyz to not be found")
	}

	// Test NewExternalSession
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sess := NewExternalSession(Config{
		Agent:      protocol.Austin,
		DriverType: "mock",
		Dir:        tmpDir,
	}, foundPath)

	if sess.DriverType() != "mock" {
		t.Fatalf("sess.DriverType() = %q, want mock", sess.DriverType())
	}
	if sess.Command() != foundPath {
		t.Fatalf("sess.Command() = %q, want %q", sess.Command(), foundPath)
	}

	if err := sess.Start(ctx); err != nil {
		t.Fatalf("failed to start external session: %v", err)
	}
	defer sess.Stop()

	// Wait for process to exit cleanly
	select {
	case <-sess.stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for external session process to exit")
	}
}
