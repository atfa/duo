package driver

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
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
//
// The install and release scripts are covered too: they used to build and install
// a named binary per driver, which put a fourth driver's name back into the edit
// set even though the behaviour was gone from Core. They now derive the list from
// `go list ./cmd/...`.
func TestCoreDoesNotEnumerateShippedDrivers(t *testing.T) {
	names := shippedDrivers(t)
	separators := []string{"|", ", ", " or ", ", and "}
	files := append(coreFiles(t), packagingFiles(t)...)
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

// packagingFiles are the scripts that decide which binaries ship. They are not
// Core, but they were a place a new driver had to be written down.
func packagingFiles(t *testing.T) []string {
	t.Helper()
	root := repoRoot(t)
	var out []string
	for _, pattern := range []string{"scripts/*.sh", ".github/workflows/*.yml"} {
		matches, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(pattern)))
		if err != nil {
			t.Fatalf("glob %s: %v", pattern, err)
		}
		for _, match := range matches {
			rel, err := filepath.Rel(root, match)
			if err != nil {
				t.Fatalf("rel %s: %v", match, err)
			}
			out = append(out, filepath.ToSlash(rel))
		}
	}
	if len(out) == 0 {
		t.Fatal("found no install or release scripts; the glob is broken")
	}
	return out
}

// TestEveryCapabilityIsBranchedOn makes the protocol's one rule about
// capabilities enforceable rather than aspirational.
//
// `activity`, `contextUsage`, `tokenRate` and `mcp` all sat in Capabilities for
// months while Core read none of them, so a plugin author following the guide was
// told a promise Duo did not keep. Prose did not stop it; this does. A field added
// here has to be read by Core or this test fails, which is the review that
// catches the next one.
//
// How it reads: a field counts as read when some Core file both talks about a
// capabilities value and qualifies that field's name. That is deliberately loose
// about *how* the field is used, so it cannot prove a branch rather than a mention
// — what it proves, reliably, is that a field nobody looks at cannot pass.
func TestEveryCapabilityIsBranchedOn(t *testing.T) {
	var fields []string
	caps := reflect.TypeOf(Capabilities{})
	for i := 0; i < caps.NumField(); i++ {
		fields = append(fields, caps.Field(i).Name)
	}
	if len(fields) == 0 {
		t.Fatal("Capabilities has no fields; the reflection below is broken")
	}

	type coreFile struct{ name, text string }
	var files []coreFile
	for _, file := range coreFiles(t) {
		files = append(files, coreFile{file, source(t, file)})
	}

	for _, field := range fields {
		read := false
		for _, file := range files {
			if mentionsCapabilities(file.text) && strings.Contains(file.text, "."+field) {
				read = true
				break
			}
		}
		if !read {
			t.Errorf("Capabilities.%s is declared but no Core file reads it; a capability Core never branches on is a promise Duo does not keep, so either branch on it or move it to the manifest as descriptive metadata", field)
		}
	}
}

func mentionsCapabilities(text string) bool {
	return strings.Contains(strings.ToLower(text), "capabilit")
}

// TestPackagingDoesNotNameDriverBinaries closes the gap the prose guard above
// cannot: a build list writes paths, not sentences. `./cmd/duo-plugin-pi
// ./cmd/duo-plugin-agy` names two drivers without ever spelling "pi, agy", so
// matching prose separators misses it entirely — which is exactly how the install
// and release scripts kept a per-driver edit set after Core stopped needing one.
//
// These tokens cannot appear in a script by accident: a script that writes
// duo-pi or duo-plugin-pi is naming a driver's binary. Both scripts now derive the
// list from `go list ./cmd/...`, so a new driver ships without being written down.
func TestPackagingDoesNotNameDriverBinaries(t *testing.T) {
	files := packagingFiles(t)
	for _, name := range shippedDrivers(t) {
		for _, token := range []string{"duo-" + name, "duo-plugin-" + name} {
			for _, file := range files {
				if strings.Contains(source(t, file), token) {
					t.Errorf("%s names the %s binary as %q; derive the binaries from `go list ./cmd/...` so a new driver needs no edit here", file, name, token)
				}
			}
		}
	}
}

// TestDocsDoNotNameACapabilityThatMoved catches the doc half of a field move.
//
// `activity` and `mcp` moved out of Capabilities onto the manifest root, and
// docs/duo-bridge-protocol.md kept telling adapter authors to set
// `capabilities.mcp` — a path that no longer exists. Prose review found it only
// because a verifier read the sentence; nothing in the build noticed. The pattern
// is specific enough to be worth checking mechanically: any `capabilities.<key>`
// in the docs must name a key Capabilities still has.
//
// CHANGELOG.md is excluded on purpose: it is the record of what changed, so it has
// to be able to name a path that no longer exists.
func TestDocsDoNotNameACapabilityThatMoved(t *testing.T) {
	capabilityKeys := jsonKeys(Capabilities{})
	moved := map[string]bool{}
	for key := range jsonKeys(Manifest{}) {
		if !capabilityKeys[key] {
			moved[key] = true
		}
	}
	if len(moved) == 0 {
		t.Fatal("every manifest key is still a capability; the moved-key set is empty, so this guard cannot fire")
	}

	pattern := regexp.MustCompile(`capabilities\.([a-zA-Z][a-zA-Z0-9]*)`)
	root := repoRoot(t)
	docs, err := filepath.Glob(filepath.Join(root, "docs", "*.md"))
	if err != nil || len(docs) == 0 {
		t.Fatalf("found no docs to check: %v", err)
	}
	for _, doc := range docs {
		rel, err := filepath.Rel(root, doc)
		if err != nil {
			t.Fatalf("rel %s: %v", doc, err)
		}
		for _, match := range pattern.FindAllStringSubmatch(source(t, filepath.ToSlash(rel)), -1) {
			if moved[match[1]] {
				t.Errorf("%s says capabilities.%s, but that is not a capability: it moved to the manifest root as descriptive metadata; a reader following this would set a field Core ignores", filepath.ToSlash(rel), match[1])
			}
		}
	}
}

// jsonKeys are the wire names a struct declares, which is what the docs name.
func jsonKeys(v any) map[string]bool {
	keys := map[string]bool{}
	t := reflect.TypeOf(v)
	for i := 0; i < t.NumField(); i++ {
		tag, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if tag != "" && tag != "-" {
			keys[tag] = true
		}
	}
	return keys
}
