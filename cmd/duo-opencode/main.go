package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/atfa/duo/internal/version"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Println("duo-opencode driver plugin " + version.Version)
		return
	}
	cmd := exec.Command("opencode", os.Args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*os.PathError); ok {
			fmt.Fprintf(os.Stderr, "duo-opencode: %v\n", exitErr)
			os.Exit(127)
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				os.Exit(status.ExitStatus())
			}
		}
		os.Exit(1)
	}
}
