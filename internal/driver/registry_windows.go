//go:build windows

package driver

import (
	"os"
	"path/filepath"
	"strings"
)

// isExecutable reports whether path is a file Windows would run. There is no
// execute bit on Windows: the extension decides, using the same PATHEXT list the
// shell searches.
func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	ext := strings.ToLower(filepath.Ext(path))
	for _, candidate := range executableExtensions() {
		if ext == candidate {
			return true
		}
	}
	return false
}

// executableSpellings returns the names a plugin may have on disk, executable
// suffix first because that is the file that exists on Windows.
func executableSpellings(names []string) []string {
	out := make([]string, 0, len(names)*2)
	for _, name := range names {
		out = append(out, name+".exe", name)
	}
	return out
}

// trimExecutableSuffix strips the extension from an executable's file name, so
// duo-plugin-pi.exe is the plugin named pi rather than one named pi.exe.
func trimExecutableSuffix(file string) string {
	return strings.TrimSuffix(file, filepath.Ext(file))
}

func executableExtensions() []string {
	var out []string
	for _, ext := range filepath.SplitList(os.Getenv("PATHEXT")) {
		if ext = strings.ToLower(strings.TrimSpace(ext)); ext != "" {
			out = append(out, ext)
		}
	}
	if len(out) == 0 {
		out = []string{".exe", ".com", ".bat", ".cmd"}
	}
	return out
}

// executableName returns the file name an executable is built as: Windows runs
// binary images by extension, so a plugin or the reference control ends in .exe.
func executableName(name string) string {
	return name + ".exe"
}
