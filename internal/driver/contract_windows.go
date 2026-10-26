//go:build windows

package driver

// crashStandIn returns a command that exits without speaking the plugin protocol.
// A shell that exits immediately is the cheapest faithful stand-in for a crashed
// plugin, and it needs nothing from the plugin under test. It cannot be a .cmd
// shim: Windows runs a batch file through the shell, not by executing it.
func crashStandIn() (string, []string) {
	return ComSpec(), []string{"/d", "/s", "/c", "exit 1"}
}
