//go:build windows

package driver

// wantEnvReference is how cmd.exe spells a variable reference in a command line.
func wantEnvReference(name string) string { return `"%` + name + `%"` }
