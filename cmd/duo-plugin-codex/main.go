// Command duo-plugin-codex is the standalone Codex Driver Plugin.
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/plugins/codex"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Printf("duo-plugin-%s %s (Duo Driver Plugin Protocol %d)\n", codex.Name, codex.DriverVersion, driver.ProtocolVersion)
		return
	}
	if err := driver.Serve(os.Stdin, os.Stdout, codex.New()); err != nil {
		log.Fatalf("duo-plugin-%s: %v", codex.Name, err)
	}
}
