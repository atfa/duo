package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/atfa/duo/internal/guiipc"
	"github.com/atfa/duo/internal/session"
)

// The GUI host is built from the same seam the terminal interface uses, and this
// assertion is where the two are proved to agree. It lives in cmd/duo rather than
// in guiipc because the host must not be able to name the live service: a package
// that can spell session.Service can hold one.
var _ guiipc.Session = (*session.Service)(nil)

const (
	// guiBinaryDefault is the renderer this build looks for: the binary name the
	// `gpui/` crate produces, so `make gpui` is enough to make `duo gui` work.
	guiBinaryDefault = "duo-gpui"
	// guiBinaryEnv names the renderer explicitly, for a build tree that is not on
	// PATH.
	guiBinaryEnv = "DUO_GPUI_BIN"
)

// runGUI starts the session `duo` would start and attaches the GPUI renderer
// instead of the terminal interface.
//
// The renderer is resolved before the session exists. A missing window must cost
// nothing: starting agents only to fail on the last step would leave a session
// behind for a command that never ran.
func runGUI(ctx context.Context, args []string) error {
	args, binary, err := parseGUIArgs(args)
	if err != nil {
		return err
	}
	// `duo gui` takes the same arguments `duo` does, resume included, so a window can
	// be opened on the session that is already in flight rather than only on a new
	// one.
	resume := len(args) > 0 && args[0] == "resume"
	if resume {
		args = args[1:]
	}
	var cfg config
	if resume {
		cfg, err = loadResumeConfig(args)
	} else {
		cfg, err = loadConfig(args)
	}
	if err != nil {
		return err
	}
	if cfg.help {
		printUsage()
		return nil
	}
	if err := cfg.validate(); err != nil {
		return err
	}
	renderer, err := findGUIBinary(binary)
	if err != nil {
		return err
	}

	return runWith(ctx, cfg, func(ctx context.Context, svc *session.Service) error {
		if err := serveGUI(ctx, svc, renderer); err != nil && ctx.Err() == nil {
			log.Printf("Duo GUI: %v", err)
		}
		return nil
	})
}

// parseGUIArgs takes the GUI-only flag out of the command line before the shared
// parser sees it. It is stripped here rather than added to parseArgs because the
// flag names a frontend, not a session, and loadConfig rejects a flag it does not
// know.
func parseGUIArgs(args []string) (rest []string, binary string, err error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--gui-binary":
			if i+1 >= len(args) {
				return nil, "", errors.New("--gui-binary requires a path")
			}
			binary = args[i+1]
			i++
		case strings.HasPrefix(arg, "--gui-binary="):
			binary = strings.TrimPrefix(arg, "--gui-binary=")
			if binary == "" {
				return nil, "", errors.New("--gui-binary requires a path")
			}
		default:
			rest = append(rest, arg)
		}
	}
	return rest, binary, nil
}

// findGUIBinary resolves the renderer: the flag, then the environment, then PATH,
// then next to the duo binary itself, so a source install keeps the two together.
func findGUIBinary(explicit string) (string, error) {
	if explicit != "" {
		return resolveGUIBinary(explicit)
	}
	if env := strings.TrimSpace(os.Getenv(guiBinaryEnv)); env != "" {
		return resolveGUIBinary(env)
	}
	if path, err := exec.LookPath(guiBinaryDefault); err == nil {
		return path, nil
	}
	if self, err := os.Executable(); err == nil {
		sibling := filepath.Join(filepath.Dir(self), guiBinaryDefault)
		if info, err := os.Stat(sibling); err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0 {
			return sibling, nil
		}
	}
	return "", fmt.Errorf("the GPUI renderer %q was not found: not --gui-binary, not $%s, not on PATH, and not next to the duo binary. Build it with `make gpui` (cargo build --release in gpui/) or name it with --gui-binary <path>", guiBinaryDefault, guiBinaryEnv)
}

// resolveGUIBinary accepts a path or a name, because an operator who names a
// binary expects the PATH lookup their shell would do.
func resolveGUIBinary(candidate string) (string, error) {
	if path, err := exec.LookPath(candidate); err == nil {
		return path, nil
	}
	return "", fmt.Errorf("the GPUI renderer %q is not an executable on PATH", candidate)
}

// serveGUI runs the renderer as a child and speaks the protocol over its stdio.
//
// The child's stdin is the display stream and its stdout is the command stream, so
// the renderer binds no socket and knows nothing about the session beyond the
// frames it reads.
func serveGUI(ctx context.Context, svc *session.Service, renderer string) error {
	cmd := exec.CommandContext(ctx, renderer)
	commands, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	display, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	// The renderer's stdout is protocol and only protocol, so its own diagnostics
	// stay on stderr, where the human can see them.
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", renderer, err)
	}

	serveErr := guiipc.NewHost(svc).Serve(ctx, commands, display)
	// The display stream ending is how the renderer learns the host is done. The
	// window stays open on the last frame until the human closes it, which is what
	// Wait waits for.
	_ = display.Close()
	waitErr := cmd.Wait()

	if serveErr != nil && ctx.Err() == nil {
		return serveErr
	}
	if waitErr != nil && ctx.Err() == nil {
		return fmt.Errorf("%s: %w", renderer, waitErr)
	}
	return nil
}
