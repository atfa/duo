//go:build windows

package driver

import (
	"context"
	"os/exec"
	"syscall"
)

// ShellCommand returns an unstarted command that runs a driver-composed shell
// command line in the operator's shell, under ctx.
//
// The command line is set verbatim rather than assembled from arguments: Go would
// escape an embedded quote as \" and cmd.exe does not treat a backslash as an
// escape, so a path with a space would arrive mangled. WindowsShellLine owns that
// detail for every caller.
func ShellCommand(ctx context.Context, command string) *exec.Cmd {
	program := ComSpec()
	cmd := exec.CommandContext(ctx, program)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: WindowsShellLine(program, command)}
	return cmd
}
