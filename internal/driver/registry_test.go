package driver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDriverNameFromExecutable(t *testing.T) {
	cases := map[string]string{
		"duo-plugin-pi":                   "pi",
		"duo-driver-agy":                  "agy",
		"duo-opencode":                    "opencode",
		"duo-plugin-codex":                "codex",
		"duo":                             "",
		"duo-mcp-server":                  "",
		"pi":                              "",
		"duo-plugin-":                     "",
		"/usr/local/bin/duo-plugin-qoder": "qoder",
	}
	for file, want := range cases {
		if got := driverNameFromExecutable(file); got != want {
			t.Errorf("driverNameFromExecutable(%q) = %q, want %q", file, got, want)
		}
	}
}

func TestReservedExecutablesAreNotPlugins(t *testing.T) {
	for _, file := range []string{"duo", "duo-mcp-server"} {
		if got := driverNameFromExecutable(file); got != "" {
			t.Errorf("%q was read as driver %q; Duo's own commands must never be plugins", file, got)
		}
	}
}

// TestLookupPrefersPluginDirOverPath checks the shadowing rule: a plugin under
// development in ~/.duo/plugins wins over a released one on PATH, which is what
// makes plugin development possible without uninstalling anything.
func TestLookupPrefersPluginDirOverPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())

	dir := filepath.Join(home, ".duo", "plugins")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "duo-plugin-example")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, ok := Lookup("example")
	if !ok || got != path {
		t.Fatalf("Lookup(example) = %q, %v; want %q, true", got, ok, path)
	}
	if _, ok := Lookup("missing"); ok {
		t.Fatal("Lookup found a driver that is not installed")
	}
}

func TestLookupRejectsNonExecutable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	dir := filepath.Join(home, ".duo", "plugins")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A data file with a plugin's name is not a plugin.
	if err := os.WriteFile(filepath.Join(dir, "duo-plugin-example"), []byte("notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := Lookup("example"); ok {
		t.Fatal("Lookup accepted a non-executable file")
	}
}

// TestDiscoverIsDeduplicatedAndSorted checks `duo plugins` output is stable: the
// same driver found twice appears once, and the list is ordered.
func TestDiscoverIsDeduplicatedAndSorted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	pathDir := t.TempDir()
	t.Setenv("PATH", pathDir)

	dir := filepath.Join(home, ".duo", "plugins")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"duo-plugin-zebra", "duo-plugin-alpha"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Same driver under the legacy name in PATH: must not appear twice.
	if err := os.WriteFile(filepath.Join(pathDir, "duo-driver-alpha"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pathDir, "duo"), nil, 0o755); err != nil {
		t.Fatal(err)
	}

	entries := Discover()
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	if strings.Join(names, ",") != "alpha,zebra" {
		t.Fatalf("Discover() = %v, want [alpha zebra]", names)
	}
	if entries[0].Source != "plugins" {
		t.Fatalf("alpha came from %q, want the plugin dir to win over PATH", entries[0].Source)
	}
}

// TestShippedIsDisplayMetadataOnly checks the shipped set is labelling, not
// behaviour: an unknown driver name is still discovered and looked up normally,
// so a fourth agent needs no change here.
func TestShippedIsDisplayMetadataOnly(t *testing.T) {
	t.Cleanup(func() { unregisterForTest("example") })
	Register("example", func() Handler { return noopDriver{} })

	if IsBuiltin("codex") {
		t.Fatal("a driver Duo does not ship must not be labelled shipped")
	}
	if !IsBuiltin("example") {
		t.Fatal("a registered driver is shipped")
	}
}

func TestNotFoundErrorNamesWhatItLookedFor(t *testing.T) {
	msg := (&NotFoundError{Driver: "codex"}).Error()
	for _, want := range []string{"duo-plugin-codex", "duo-driver-codex", "duo-codex"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message %q does not name %q", msg, want)
		}
	}
}

// noopDriver is a Handler that answers only what the protocol requires, used to
// exercise registration without pulling in a real driver's behaviour.
type noopDriver struct{}

func (noopDriver) Describe() (*Manifest, error) {
	return &Manifest{
		Protocol:       ProtocolVersion,
		Name:           "example",
		Version:        "0.0.1",
		ModelReference: ModelQualified,
		Capabilities:   Capabilities{Resume: ResumeNone, Bridge: BridgeNone},
	}, nil
}
func (noopDriver) Probe() (*ProbeResult, error) { return &ProbeResult{}, nil }
func (noopDriver) Prepare(LaunchRequest) (*LaunchPlan, error) {
	return &LaunchPlan{Command: "example"}, nil
}

// unregisterForTest removes a registration so one test cannot leak into another.
func unregisterForTest(name string) {
	builtinMu.Lock()
	defer builtinMu.Unlock()
	delete(builtins, name)
}

// TestResolvePrefersTheExecutableOverTheBuiltin is the migration guarantee: an
// installed plugin is the implementation that runs, and removing it falls back to
// the identical Handler in-process. `--agent <name>` therefore behaves the same
// either way, which is what makes converting a driver to a real plugin safe to ship
// before every machine has one.
func TestResolvePrefersTheExecutableOverTheBuiltin(t *testing.T) {
	t.Cleanup(func() { unregisterForTest("example") })
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	dir := filepath.Join(home, ".duo", "plugins")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	plugin := filepath.Join(dir, "duo-plugin-example")
	if err := os.WriteFile(plugin, nil, 0o755); err != nil {
		t.Fatal(err)
	}

	Register("example", func() Handler { return noopDriver{} })
	caller, err := Resolve(context.Background(), "example", nil, nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	defer caller.Close()
	if _, ok := caller.(*Builtin); ok {
		t.Fatal("an installed plugin was ignored in favour of the in-process driver")
	}

	// Removing the plugin must fall back rather than fail.
	if err := os.Remove(plugin); err != nil {
		t.Fatal(err)
	}
	fallback, err := Resolve(context.Background(), "example", nil, nil)
	if err != nil {
		t.Fatalf("resolve without a plugin: %v", err)
	}
	defer fallback.Close()
	if _, ok := fallback.(*Builtin); !ok {
		t.Fatalf("resolve chose %T, want the in-process driver", fallback)
	}
}

// TestDiscoverReportsWhicheverImplementationRuns keeps `duo plugins` honest: the
// path it prints is the one that will be executed, not a leftover from another
// install.
func TestDiscoverReportsWhicheverImplementationRuns(t *testing.T) {
	t.Cleanup(func() { unregisterForTest("example") })
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	Register("example", func() Handler { return noopDriver{} })

	entries := Discover()
	if len(entries) != 1 || entries[0].Name != "example" || entries[0].Path != BuiltInSource {
		t.Fatalf("Discover() = %+v, want the in-process driver", entries)
	}

	dir := filepath.Join(home, ".duo", "plugins")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	plugin := filepath.Join(dir, "duo-plugin-example")
	if err := os.WriteFile(plugin, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	entries = Discover()
	if len(entries) != 1 || entries[0].Path != plugin {
		t.Fatalf("Discover() = %+v, want the installed plugin at %q", entries, plugin)
	}
	if !entries[0].Shipped {
		t.Error("a registered driver is still shipped, plugin or not")
	}
}

// TestResolveReportsAnUnknownDriverByName checks the message names the files
// Core looked for, since the fix is either installing or renaming the plugin.
func TestResolveReportsAnUnknownDriverByName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	_, err := Resolve(context.Background(), "codex", nil, nil)
	if err == nil {
		t.Fatal("resolving an uninstalled driver must fail")
	}
	var notFound *NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("error = %v, want a *NotFoundError", err)
	}
	if notFound.Driver != "codex" {
		t.Fatalf("error names driver %q, want codex", notFound.Driver)
	}
}
