// Command duo-plugin-agy is the Agy Driver Plugin for Duo Core, as a standalone
// executable.
//
// The Handler in plugins/agy is the whole implementation — transcript observer
// and upstream bridge client included; this main is the two lines that expose it
// over the Duo Driver Plugin Protocol. Nothing about Agy lives here, which is the
// point: `duo --agent agy` reaches Agy through exactly the same contract a
// third-party plugin would implement, and Duo Core has no idea which is which.
//
// Run it by hand to see what Core sees:
//
//	duo-plugin-agy --version
//	echo '{"protocol":1,"id":1,"method":"describe"}' | duo-plugin-agy
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/plugins/agy"
)

func main() {
	// --version answers without touching the protocol, so an operator can check
	// which plugin build is installed without starting one.
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Printf("%s %s (Duo Driver Plugin Protocol %d)\n", "duo-plugin-"+agy.Name, agy.DriverVersion, driver.ProtocolVersion)
		return
	}
	if err := driver.Serve(os.Stdin, os.Stdout, agy.New()); err != nil {
		log.Fatalf("duo-plugin-%s: %v", agy.Name, err)
	}
}
