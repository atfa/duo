package main

import (
	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/plugins/agy"
	"github.com/atfa/duo/plugins/opencode"
	"github.com/atfa/duo/plugins/pi"
)

// registerDrivers is Duo Core's entire knowledge of which coding agents it ships.
//
// Each driver is a Driver Plugin that happens to run in Core's own process for now;
// the same Handler will serve a standalone `duo-plugin-<name>` executable with no
// change. Registering one here is a single line, and it says only "Duo ships this".
// What the driver does comes from the manifest it returns, which is why adding a
// fourth agent is one line here and nothing at all anywhere else.
func registerDrivers() {
	pi.Register()
	agy.Register()
	opencode.Register()
}

// shippedDrivers lists the driver names Duo ships, for `duo --help` and for the
// error message when a name is not installed.
func shippedDrivers() []string { return driver.BuiltinNames() }
