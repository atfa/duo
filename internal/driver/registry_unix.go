//go:build !windows

package driver

import "os"

// isExecutable reports whether path is a file this platform would run. Unix marks
// that with the execute bit.
func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode()&0111 != 0
}

// executableSpellings returns the names a plugin may have on disk. Unix has no
// mandatory suffix, so the names are used as they are.
func executableSpellings(names []string) []string {
	return names
}

// trimExecutableSuffix strips the suffix a Windows executable would carry. On Unix
// there is nothing to strip.
func trimExecutableSuffix(file string) string {
	return file
}

// executableName returns the file name an executable is built as. Unix has no
// mandatory suffix, so the name is used as given.
func executableName(name string) string {
	return name
}
