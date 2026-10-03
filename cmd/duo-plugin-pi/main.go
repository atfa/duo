// Command duo-plugin-pi is the Pi Driver Plugin for Duo Core, as a standalone
// executable.
//
// The Handler in plugins/pi is the whole implementation; this main is the two lines
// that expose it over the Duo Driver Plugin Protocol. Nothing about Pi lives here,
// which is the point: `duo --agent pi` now reaches Pi through exactly the same
// contract a third-party plugin for Codex or Claude Code would implement, and Duo
// Core has no idea which is which.
//
// Run it by hand to see what Core sees:
//
//	duo-plugin-pi --version
//	echo '{"protocol":1,"id":1,"method":"describe"}' | duo-plugin-pi
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/plugins/pi"
)

func main() {
	// --version answers without touching the protocol, so an operator can check
	// which plugin build is installed without starting one.
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Printf("%s %s (Duo Driver Plugin Protocol %d)\n", "duo-plugin-"+pi.Name, pi.DriverVersion, driver.ProtocolVersion)
		return
	}
	if err := driver.Serve(os.Stdin, os.Stdout, pi.New()); err != nil {
		log.Fatalf("duo-plugin-%s: %v", pi.Name, err)
	}
}
