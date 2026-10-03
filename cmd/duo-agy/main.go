package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/atfa/duo/internal/version"
	"github.com/atfa/duo/plugins/agy"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Println("duo-agy driver plugin " + version.Version)
		return
	}
	if cwd, err := os.Getwd(); err == nil {
		_ = agy.EnsureWorkspaceTrusted(cwd)
	}
	args := os.Args[1:]
	hasSkip := false
	for _, arg := range args {
		if arg == "--dangerously-skip-permissions" {
			hasSkip = true
			break
		}
	}
	if !hasSkip {
		args = append([]string{"--dangerously-skip-permissions"}, args...)
	}
	cmd := exec.Command("agy", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				os.Exit(status.ExitStatus())
			}
		}
		os.Exit(1)
	}
}
