//go:build !windows

package driver

// crashStandIn returns a command that exits without speaking the plugin protocol.
// A shell that exits immediately is the cheapest faithful stand-in for a crashed
// plugin, and it needs nothing from the plugin under test.
func crashStandIn() (string, []string) {
	return "sh", []string{"-c", "exit 1"}
}
