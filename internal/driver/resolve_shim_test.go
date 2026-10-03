package driver

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// legacyShim writes an executable that is not a Driver Plugin: it execs its
// argument and speaks no protocol at all, which is exactly what the shipped
// duo-pi/duo-agy/duo-opencode shims are for one release.
func legacyShim(t *testing.T, dir, name string) {
	t.Helper()
	path := filepath.Join(dir, name)
	script := "#!/bin/sh\nexit 0\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestResolveIgnoresALegacyShim is the promise Resolve makes in its own comment:
// uninstalling the plugin falls back to the identical Handler inside Duo, so
// --agent pi never depends on which is present. A machine holding the 0.9-era shim
// but not the new plugin is that state, and spawning the shim as a plugin fails
// the describe call outright instead of falling back.
func TestResolveIgnoresALegacyShim(t *testing.T) {
	bin := t.TempDir()
	legacyShim(t, bin, "duo-pi")

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin)

	Register("pi", func() Handler { return shimHandler{} })

	caller, err := Resolve(context.Background(), "pi", nil, func(error) {})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, ok := caller.(*Builtin); !ok {
		t.Fatalf("resolve ran the legacy shim as a plugin (%T); it speaks no protocol", caller)
	}
}

// TestResolvePrefersTheCanonicalPluginOverAShim covers the ordinary upgrade: both
// present, and the real plugin must win.
func TestResolvePrefersTheCanonicalPluginOverAShim(t *testing.T) {
	bin := t.TempDir()
	legacyShim(t, bin, "duo-pi")
	real := filepath.Join(bin, "duo-plugin-pi")
	if err := os.WriteFile(real, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", bin)

	caller, err := Resolve(context.Background(), "pi", nil, func(error) {})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, ok := caller.(*Supervised); !ok {
		t.Fatalf("resolve ignored the canonical plugin and ran something else (%T)", caller)
	}
}

type shimHandler struct{}

func (shimHandler) Describe() (*Manifest, error) {
	return &Manifest{Protocol: ProtocolVersion, Name: "pi", ModelReference: ModelBare}, nil
}
func (shimHandler) Probe() (*ProbeResult, error) { return &ProbeResult{Available: true}, nil }
func (shimHandler) Prepare(LaunchRequest) (*LaunchPlan, error) {
	return &LaunchPlan{Command: "sh -c 'exit 0'"}, nil
}

// TestDiscoverReportsTheExecutableResolveWouldRun closes a display/resolve mismatch
// a fresh install can show: with duo-plugin-pi and the 0.9-era duo-pi both present,
// `duo plugins` listed the shim while the session ran the real plugin, because
// ReadDir order is alphabetical and duo-pi sorts first.
func TestDiscoverReportsTheExecutableResolveWouldRun(t *testing.T) {
	bin := t.TempDir()
	legacyShim(t, bin, "duo-pi")
	real := filepath.Join(bin, "duo-plugin-pi")
	if err := os.WriteFile(real, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", bin)

	for _, entry := range Discover() {
		if entry.Name != "pi" {
			continue
		}
		want, found := Lookup("pi")
		if !found {
			t.Fatal("lookup found nothing")
		}
		if entry.Path != want {
			t.Fatalf("duo plugins would report %s while the session runs %s", entry.Path, want)
		}
		return
	}
	t.Fatal("pi was not discovered at all")
}
