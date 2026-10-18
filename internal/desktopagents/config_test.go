package desktopagents

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDiscoverPathReadsUserShellPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not use a POSIX login shell")
	}
	shell := fakeShell(t)
	t.Setenv("SHELL", shell)
	t.Setenv("XDUO_TEST_SHELL_OUTPUT", "startup output\n__XDUO_PATH__/home/test/.local/bin:/usr/bin")

	got, err := discoverPath(context.Background())
	if err != nil {
		t.Fatalf("discoverPath() error = %v", err)
	}
	want := "/home/test/.local/bin:/usr/bin"
	if got != want {
		t.Fatalf("discoverPath() = %q, want %q", got, want)
	}
}

func TestRefreshFallsBackWhenUserShellPathIsUnavailable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not use a POSIX login shell")
	}

	for _, tc := range []struct {
		name   string
		output string
		exit   string
	}{
		{name: "shell failure", output: "", exit: "1"},
		{name: "missing path marker", output: "startup output", exit: "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shell := fakeShell(t)
			t.Setenv("SHELL", shell)
			t.Setenv("XDUO_TEST_SHELL_OUTPUT", tc.output)
			t.Setenv("XDUO_TEST_SHELL_EXIT", tc.exit)
			currentPath := filepath.Join(t.TempDir(), "desktop-bin")
			savedPath := filepath.Join(t.TempDir(), "saved-bin")
			t.Setenv("PATH", currentPath)

			registry := &Registry{
				dir: filepath.Join(t.TempDir(), "xduo"),
				state: State{
					Version: configVersion,
					Path:    savedPath,
					Agents:  map[string]Agent{},
				},
			}
			state, err := registry.Refresh(context.Background(), nil)
			if err != nil {
				t.Fatalf("Refresh() error = %v", err)
			}
			wantPath := joinPath(savedPath, currentPath)
			if state.Path != wantPath {
				t.Fatalf("Refresh().Path = %q, want fallback %q", state.Path, wantPath)
			}
			if !strings.Contains(state.ScanError, "used the saved and current PATH") {
				t.Fatalf("Refresh().ScanError = %q, want fallback explanation", state.ScanError)
			}
		})
	}
}

func TestRefreshUsesShellProvidedExecutablePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not use a POSIX login shell")
	}

	bin := t.TempDir()
	cli := filepath.Join(bin, "pi")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	shell := fakeShell(t)
	t.Setenv("SHELL", shell)
	t.Setenv("XDUO_TEST_SHELL_OUTPUT", "__XDUO_PATH__"+bin)
	t.Setenv("XDUO_TEST_SHELL_EXIT", "0")
	t.Setenv("PATH", t.TempDir()) // The desktop process PATH does not include the installed agent.

	registry := &Registry{
		dir:   filepath.Join(t.TempDir(), "xduo"),
		state: State{Version: configVersion, Agents: map[string]Agent{}},
	}
	state, err := registry.Refresh(context.Background(), []string{"pi"})
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if got := state.Agents["pi"].Path; got != cli {
		t.Fatalf("Refresh() discovered pi at %q, want %q", got, cli)
	}
}

func fakeShell(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-shell")
	script := "#!/bin/sh\nprintf '%s\\n' \"$XDUO_TEST_SHELL_OUTPUT\"\nexit \"${XDUO_TEST_SHELL_EXIT:-0}\"\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}
