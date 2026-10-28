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
		args := []string{"new-session", "-d", "-s", "xduo-manager", "-e", "XDUO_MANAGER=1", executable}
		if duo != "" {
			args = append(args, "-duo", duo)
		}
		if out, e := run(false, args...); e != nil {
			return false, fmt.Errorf("xduo: create manager: %w: %s", e, strings.TrimSpace(string(out)))
		}
	}
	if tmuxEnv != "" {
		_, err = run(false, "switch-client", "-t", "=xduo-manager")
	} else {
		_, err = run(true, "attach-session", "-t", "=xduo-manager")
	}
	if err != nil {
		return false, fmt.Errorf("xduo: open manager: %w", err)
	}
	return true, nil
}
