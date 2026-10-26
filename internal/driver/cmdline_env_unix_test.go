//go:build !windows

package driver

// wantEnvReference is how /bin/sh spells a variable reference in a command line.
func wantEnvReference(name string) string { return `"$` + name + `"` }
