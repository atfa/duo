// Command duo-plugin-opencode is the opencode Driver Plugin for Duo Core, as a
// standalone executable.
//
// The Handler in plugins/opencode is the whole implementation. This main only
// exposes it over the Duo Driver Plugin Protocol, which is what makes `duo --agent
// opencode` indistinguishable from a plugin a third party wrote for Codex or Claude
// Code: Duo Core resolves a name, asks it to describe itself, and works from the
// answer.
//
// Run it by hand to see what Core sees:
//
//	duo-plugin-opencode --version
//	echo '{"protocol":1,"id":1,"method":"describe"}' | duo-plugin-opencode
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/plugins/opencode"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Printf("duo-plugin-%s %s (Duo Driver Plugin Protocol %d)\n", opencode.Name, opencode.DriverVersion, driver.ProtocolVersion)
		return
	}
	if err := driver.Serve(os.Stdin, os.Stdout, opencode.New()); err != nil {
		log.Fatalf("duo-plugin-%s: %v", opencode.Name, err)
	}
}
