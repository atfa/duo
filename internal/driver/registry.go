package driver

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// executablesFor returns the file names a plugin may be installed under, most
// specific first. duo-plugin-<name> is canonical; duo-driver-<name> and
// duo-<name> are accepted so a driver written against Duo 0.9's naming keeps
// working without a reinstall.
func executablesFor(name string) []string {
	return []string{"duo-plugin-" + name, "duo-driver-" + name, "duo-" + name}
}

// PluginDir is where user-installed plugins live. It takes precedence over
// $PATH, so a plugin under development shadows a released one.
func PluginDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".duo", "plugins")
}

// Lookup finds the executable implementing the named driver. It returns the path
// and whether one was found; the caller decides what a missing plugin means.
func Lookup(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false
	}
	candidates := executablesFor(name)
	if dir := PluginDir(); dir != "" {
		for _, candidate := range candidates {
			path := filepath.Join(dir, candidate)
			if isExecutable(path) {
				return path, true
			}
		}
	}
	for _, candidate := range candidates {
		if path, err := exec.LookPath(candidate); err == nil && isExecutable(path) {
			return path, true
		}
	}
	return "", false
}

// Entry is one discovered driver plugin.
type Entry struct {
	// Name is the driver name, i.e. what `duo --agent <name>` selects.
	Name string
	// Path is the executable that implements it, or BuiltInSource for a driver Duo
	// runs in its own process.
	Path string
	// Shipped reports whether Duo ships this driver, which only affects how
	// `duo plugins` labels it.
	Shipped bool
	// Source is where the executable was found: "plugins", "path" or "duo".
	Source string
}

// BuiltInSource is the Path reported for a driver Duo runs in its own process.
const BuiltInSource = "(built-in)"

// Discover returns every driver available to this Duo, shipped or external.
//
// A shipped driver is reported from its registration rather than from a file on
// disk: that is the implementation that will actually run, and a same-named
// executable left over from an earlier install must not shadow it. External
// discovery is by executable name alone — nothing is executed and nothing is asked
// — so listing plugins can never hang on a broken plugin.
func Discover() []Entry {
	seen := make(map[string]bool)
	var out []Entry
	for _, name := range BuiltinNames() {
		seen[name] = true
		out = append(out, Entry{Name: name, Path: BuiltInSource, Shipped: true, Source: "duo"})
	}
	add := func(name, path, source string) {
		if name == "" || seen[name] || !isExecutable(path) {
			return
		}
		seen[name] = true
		out = append(out, Entry{Name: name, Path: path, Source: source})
	}

	if dir := PluginDir(); dir != "" {
		if entries, err := os.ReadDir(dir); err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				add(driverNameFromExecutable(entry.Name()), filepath.Join(dir, entry.Name()), "plugins")
			}
		}
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			add(driverNameFromExecutable(entry.Name()), filepath.Join(dir, entry.Name()), "path")
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// reservedExecutables are Duo's own commands, which must never be mistaken for a
// driver plugin just because they live in the same directory on PATH.
var reservedExecutables = map[string]bool{
	"duo":             true,
	"duo-mcp-server":  true,
	"duo-plugin-test": true,
}

// driverNameFromExecutable extracts a driver name from a plugin file name, or
// "" when the file is not a plugin at all.
func driverNameFromExecutable(file string) string {
	base := strings.TrimPrefix(filepath.Base(file), ".exe")
	switch {
	case strings.HasPrefix(base, "duo-plugin-"):
		base = strings.TrimPrefix(base, "duo-plugin-")
	case strings.HasPrefix(base, "duo-driver-"):
		base = strings.TrimPrefix(base, "duo-driver-")
	case strings.HasPrefix(base, "duo-"):
		base = strings.TrimPrefix(base, "duo-")
	default:
		return ""
	}
	if base == "" || reservedExecutables[file] {
		return ""
	}
	return base
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode()&0111 != 0
}

// NotFoundError reports a driver name that no plugin implements. It names the
// exact file names Core looked for, because the fix for an operator is either
// "install it" or "rename it", and neither is guessable from "unknown driver".
type NotFoundError struct {
	Driver string
}

func (e *NotFoundError) Error() string {
	names := executablesFor(e.Driver)
	return "no driver plugin for " + e.Driver + ": looked for " +
		strings.Join(names, " and ") + " in " + PluginDir() + " and on PATH"
}
