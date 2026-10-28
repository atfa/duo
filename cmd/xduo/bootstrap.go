package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/term"
)

type tmuxRunner func(interactive bool, args ...string) ([]byte, error)

func terminalReady() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

func runTmux(interactive bool, args ...string) ([]byte, error) {
	path, err := exec.LookPath("tmux")
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(path, args...)
	if interactive {
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return nil, cmd.Run()
	}
	return cmd.CombinedOutput()
}

// bootstrap starts or joins a dedicated manager without nesting tmux.
func bootstrap(duo, tmuxEnv, manager string, tty bool, run tmuxRunner) (bool, error) {
	if !tty {
		return false, fmt.Errorf("xduo requires an interactive terminal")
	}
	if manager == "1" {
		return false, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return false, err
	}
	if tmuxEnv != "" {
		current, err := run(false, "display-message", "-p", "#{session_name}")
		if err != nil {
			return false, fmt.Errorf("xduo: inspect tmux client: %w", err)
		}
		if strings.TrimSpace(string(current)) == "xduo-manager" {
			return false, nil
		}
	}
	_, err = run(false, "has-session", "-t", "=xduo-manager")
	if err != nil {
		cwd, err := os.Getwd()
		if err != nil {
			return false, err
		}
		args := []string{"new-session", "-c", cwd, "-d", "-s", "xduo-manager", "-e", "XDUO_MANAGER=1", executable}
		if duo != "" {
			args = append(args, "-duo", duo)
		}
		if out, e := run(false, args...); e != nil {
			return false, fmt.Errorf("xduo: create manager: %w: %s", e, strings.TrimSpace(string(out)))
		}
	}
	if out, err := run(false, "set-option", "-t", "xduo-manager", "status-left-length", "32"); err != nil {
		return false, fmt.Errorf("xduo: configure manager status: %w: %s", err, strings.TrimSpace(string(out)))
	}
	var out []byte
	if tmuxEnv != "" {
		out, err = run(false, "switch-client", "-t", "=xduo-manager")
	} else {
		out, err = run(true, "attach-session", "-t", "=xduo-manager")
	}
	if err != nil {
		return false, fmt.Errorf("xduo: open manager: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return true, nil
}
