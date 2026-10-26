//go:build !windows

package agent

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
)

// makePollablePTY rewraps a nonblocking duplicate so Go's poller can interrupt
// blocked PTY I/O on close.
func makePollablePTY(ptmx *os.File) (*os.File, error) {
	raw, err := ptmx.SyscallConn()
	if err != nil {
		return nil, err
	}
	var fd int
	var dupErr error
	if err := raw.Control(func(rawFD uintptr) {
		fd, dupErr = syscall.Dup(int(rawFD))
	}); err != nil {
		return nil, err
	}
	if dupErr != nil {
		return nil, dupErr
	}
	syscall.CloseOnExec(fd)
	if err := syscall.SetNonblock(fd, true); err != nil {
		_ = syscall.Close(fd)
		return nil, err
	}
	file := os.NewFile(uintptr(fd), ptmx.Name())
	if file == nil {
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("wrap duplicated PTY master")
	}
	if err := ptmx.Close(); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

// ptyTerminal is a Unix pseudo-terminal: one file descriptor carries both the
// agent's output and its input.
type ptyTerminal struct{ file *os.File }

func (t *ptyTerminal) Read(p []byte) (int, error)  { return t.file.Read(p) }
func (t *ptyTerminal) Write(p []byte) (int, error) { return t.file.Write(p) }
func (t *ptyTerminal) Close() error                { return t.file.Close() }

func (t *ptyTerminal) Resize(cols, rows int) error {
	return pty.Setsize(t.file, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

type unixProcess struct {
	cmd *exec.Cmd
	pty *ptyTerminal
}

// startProcess runs command in a shell inside a new PTY. The shell preserves the
// existing DUO_PI_COMMAND behaviour (including arguments) and expands any variable
// the plugin's own environment supplies.
func startProcess(command, dir string, env []string, cols, rows int) (runningProcess, error) {
	cmd := exec.Command("sh", "-lc", "exec "+command)
	cmd.Dir = dir
	cmd.Env = env

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, fmt.Errorf("start PTY: %w", err)
	}
	pollable, err := makePollablePTY(ptmx)
	if err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = ptmx.Close()
		_ = cmd.Wait()
		return nil, fmt.Errorf("prepare PTY for I/O: %w", err)
	}
	return &unixProcess{cmd: cmd, pty: &ptyTerminal{file: pollable}}, nil
}

func (p *unixProcess) term() terminal { return p.pty }

func (p *unixProcess) wait() error { return p.cmd.Wait() }

// interrupt signals the process group, not just the shell: the shell execs the
// command, but a command that starts children of its own must take them down too.
func (p *unixProcess) interrupt() {
	_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM)
}

func (p *unixProcess) kill() {
	_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
}

// close has nothing to release: cmd.Wait and the terminal own every descriptor.
func (p *unixProcess) close() {}
