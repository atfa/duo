package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

const rendererName = "xduo-renderer"

func main() {
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "xduo:", err)
		os.Exit(1)
	}
	command, args, env, err := launch(runtime.GOOS, executable, os.Args[1:], os.Getenv, exec.LookPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "xduo:", err)
		os.Exit(1)
	}
	cmd := exec.Command(command, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = env
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "xduo:", err)
		os.Exit(1)
	}
}

func launch(goos, executable string, args []string, getenv func(string) string, lookPath func(string) (string, error)) (string, []string, []string, error) {
	self, err := filepath.Abs(executable)
	if err != nil {
		return "", nil, nil, err
	}
	dir := filepath.Dir(self)
	if goos == "darwin" && getenv("DUO_GPUI_BIN") == "" {
		bundle := ""
		if data, readErr := os.ReadFile(filepath.Join(dir, "xduo.app-path")); readErr == nil {
			bundle = string(data)
			if info, statErr := os.Stat(bundle); statErr != nil || !info.IsDir() {
				return "", nil, nil, fmt.Errorf("installed xduo app bundle is missing: %s", bundle)
			}
		} else if !os.IsNotExist(readErr) {
			return "", nil, nil, fmt.Errorf("read installed xduo app path: %w", readErr)
		} else {
			candidate := filepath.Join(dir, "xduo.app")
			if info, statErr := os.Stat(candidate); statErr == nil && info.IsDir() {
				bundle = candidate
			}
		}
		if bundle != "" {
			open := "/usr/bin/open"
			return open, append([]string{"-a", bundle, "--args"}, args...), os.Environ(), nil
		}
	}

	duo := filepath.Join(dir, "duo")
	if !executableFile(duo) {
		duo, err = lookPath("duo")
		if err != nil {
			return "", nil, nil, fmt.Errorf("duo was not found next to xduo or on PATH")
		}
	}
	renderer := getenv("DUO_GPUI_BIN")
	if renderer == "" {
		renderer = filepath.Join(dir, rendererName)
		if !executableFile(renderer) {
			renderer, err = lookPath(rendererName)
			if err != nil {
				return "", nil, nil, fmt.Errorf("the graphical renderer %q was not found; install xduo-renderer or set DUO_GPUI_BIN", rendererName)
			}
		}
	} else if !executableFile(renderer) {
		if path, lookupErr := lookPath(renderer); lookupErr == nil {
			renderer = path
		} else {
			return "", nil, nil, fmt.Errorf("DUO_GPUI_BIN is not executable: %s", renderer)
		}
	}
	env := os.Environ()
	env = setEnv(env, "DUO_GPUI_BIN", renderer)
	return duo, append([]string{"gui", "--welcome"}, args...), env, nil
}

func executableFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0
}

func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i := range env {
		if len(env[i]) >= len(prefix) && env[i][:len(prefix)] == prefix {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}
