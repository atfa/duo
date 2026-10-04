package driver

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// compositionRoot is the one file allowed to wire in a driver, because it is the
// one place that declares "Duo ships this" and nothing about how it behaves.
const compositionRoot = "cmd/duo/drivers.go"

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("no go.mod at %s, so this test is not running in the repository: %v", root, err)
	}
	return root
}

func source(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

func goFilesUnder(t *testing.T, dir string) []string {
	t.Helper()
	root := repoRoot(t)
	var out []string
	err := filepath.WalkDir(filepath.Join(root, filepath.FromSlash(dir)), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return out
}

// coreFiles is every non-test Go file in internal/ and cmd/duo: the code that must
// not have to change when a driver is added. The other cmd/ directories are
// plugin-side entry points — a plugin binary importing its own driver is the
// point of a plugin, not a dependency of Core on drivers.
func coreFiles(t *testing.T) []string {
	t.Helper()
	files := append(goFilesUnder(t, "internal"), goFilesUnder(t, "cmd/duo")...)
	if len(files) == 0 {
		t.Fatal("found no core Go files; the walk is broken")
	}
	return files
}

// shippedDrivers are the drivers developed in tree, read from the plugins
// directory rather than written down here, so the guards below follow a new
// driver automatically instead of needing this test edited alongside it.
func shippedDrivers(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(repoRoot(t), "plugins"))
	if err != nil {
		t.Fatalf("read plugins/: %v", err)
	}
	var out []string
	for _, entry := range entries {
		if entry.IsDir() {
			out = append(out, entry.Name())
		}
	}
	slices.Sort(out)
	if len(out) < 2 {
		t.Fatalf("plugins/ holds %v; the enumeration guards need at least two drivers to mean anything", out)
	}
	return out
}

// TestCoreNeverImportsADriverPlugin is the invariant that lets a new driver be
// written entirely under plugins/: Core may not reach into a driver package to
// learn anything, because whatever Core learns that way has to be written again
// for the next driver. The manifest is the only channel; the composition root is
// allowed to import drivers because that is where shipping them is declared.
func TestCoreNeverImportsADriverPlugin(t *testing.T) {
	const pluginImport = `"github.com/atfa/duo/plugins/`
	for _, file := range coreFiles(t) {
		if file == compositionRoot {
			continue
		}
		if strings.Contains(source(t, file), pluginImport) {
			t.Errorf("%s imports a driver plugin package; only %s may, so that adding a driver never requires a change in Core", file, compositionRoot)
		}
	}
}

// TestShippedDriversAreWiredInAtOneSite pins the other half of the contract: one
// line in the composition root is the whole integration surface for a driver Duo
// ships. A second site that names drivers — a switch on a driver name, a
// per-driver table — is how Core starts branching on driver identity again, which
// is exactly what reading the manifest exists to prevent.
func TestShippedDriversAreWiredInAtOneSite(t *testing.T) {
	sites := map[string][]string{}
	for _, file := range coreFiles(t) {
		for _, line := range strings.Split(source(t, file), "\n") {
			// A driver's own Register() is how the composition root wires one in.
			// driver.Register is the registry it calls inside the plugin.
			if strings.Contains(line, ".Register(") && !strings.Contains(line, "driver.Register(") {
				sites[file] = append(sites[file], strings.TrimSpace(line))
			}
		}
	}
	lines, ok := sites[compositionRoot]
	if !ok || len(lines) == 0 {
		found := make([]string, 0, len(sites))
		for file := range sites {
			found = append(found, file)
		}
		t.Fatalf("%s registers no driver (found calls in %v); it is the single place a shipped driver is wired in", compositionRoot, found)
	}
	if len(sites) != 1 {
		found := make([]string, 0, len(sites))
		for file := range sites {
			found = append(found, file)
		}
		t.Fatalf("driver.Register is called from %v; adding a driver must need an edit in %s and nowhere else", found, compositionRoot)
	}
	for _, line := range lines {
		if !strings.HasSuffix(line, ".Register()") {
			t.Errorf("%s registers a driver with %q; call the plugin's own Register() so the call site carries no driver-specific argument", compositionRoot, line)
		}
	}
	if got := len(lines); got != len(shippedDrivers(t)) {
		t.Errorf("%s registers %d drivers but plugins/ holds %d; each driver developed in tree is registered there", compositionRoot, got, len(shippedDrivers(t)))
	}
}

// TestCoreDoesNotEnumerateShippedDrivers keeps the user-facing surfaces free of a
// written-down driver list. Such a list is somewhere Core has to remember to
// update, so it silently goes stale on the next driver and then contradicts
// `duo plugins`. Matching adjacent pairs rather than the whole list keeps the
// guard independent of the order the names happen to be written in, and still
// catches an enumeration of more drivers than Core knows about.
func TestCoreDoesNotEnumerateShippedDrivers(t *testing.T) {
	names := shippedDrivers(t)
	separators := []string{"|", ", ", " or ", ", and "}
	files := coreFiles(t)
	for i, first := range names {
		for j, second := range names {
			if i == j {
				continue
			}
			for _, sep := range separators {
				pair := first + sep + second
				for _, file := range files {
					if strings.Contains(source(t, file), pair) {
						t.Errorf("%s spells out shipped drivers as %q; name the flag and point at `duo plugins` instead, so a new driver needs no edit here", file, pair)
					}
				}
			}
		}
	}
}
