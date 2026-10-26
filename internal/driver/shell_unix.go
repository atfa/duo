//go:build !windows

package driver

import (
	"context"
	"os/exec"
)

// ShellCommand returns an unstarted command that runs a driver-composed shell
// command line in the operator's shell, under ctx.
//
// The shell is the platform's, because a launch command is a complete shell line
// by contract: a Windows agent is not started by /bin/sh and a Unix agent is not
// started by cmd.exe.
func ShellCommand(ctx context.Context, command string) *exec.Cmd {
	// The shell line is the plugin's own launch command — the documented contract
	// of LaunchPlan.Command — not data from a message or a remote peer.
	// pi-lens-ignore: go-command-injection
	return exec.CommandContext(ctx, "sh", "-c", command)
}
