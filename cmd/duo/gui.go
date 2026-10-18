package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/atfa/duo/internal/guiipc"
	"github.com/atfa/duo/internal/session"
	"github.com/atfa/duo/internal/workspace"
)

// The GUI host is built from the same seam the terminal interface uses, and this
// assertion is where the two are proved to agree. It lives in cmd/duo rather than
// in guiipc because the host must not be able to name the live service: a package
// that can spell session.Service can hold one.
var _ guiipc.Session = (*session.Service)(nil)

const (
	// guiBinaryDefault is the renderer binary produced by the `gpui/` crate, so
	// `make gpui` is enough to make `duo gui` work.
	guiBinaryDefault = "xduo"
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
	welcome := false
	filtered := args[:0]
	for _, arg := range args {
		if arg == "--welcome" {
			welcome = true
		} else {
			filtered = append(filtered, arg)
		}
	}
	args = filtered
	if welcome {
		if len(args) != 0 {
			return errors.New("duo gui --welcome does not accept a repository argument")
		}
		renderer, err := findGUIBinary(binary)
		if err != nil {
			return err
		}
		return serveWelcomeGUI(ctx, renderer)
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

func serveWelcomeGUI(ctx context.Context, renderer string) error {
	cmd := exec.CommandContext(ctx, renderer)
	commands, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	display, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", renderer, err)
	}
	reader := bufio.NewReader(commands)
	defer func() { _ = display.Close(); _ = cmd.Wait() }()
	if err := json.NewEncoder(display).Encode(map[string]any{"type": "view", "view": map[string]any{"language": "en"}}); err != nil {
		return err
	}
	for {
		line, err := readWelcomeCommand(reader)
		if errors.Is(err, errGUICommandTooLarge) {
			_ = json.NewEncoder(display).Encode(map[string]string{"type": "error", "text": "GUI command exceeds 1 MiB"})
			continue
		}
		if err != nil {
			return nil
		}
		var command guiipc.Command
		if json.Unmarshal(line, &command) != nil || command.Type != "command" {
			continue
		}
		if command.Kind == guiipc.KindQuit {
			return nil
		}
		if command.Kind != guiipc.KindOpenProject {
			continue
		}
		root, err := workspace.FindRoot(ctx, command.Path)
		resumeID := ""
		if err == nil {
			resumeID, err = chooseGUISession(ctx, root)
		}
		if err == nil {
			var cfg config
			if resumeID != "" {
				cfg, err = loadResumeConfig([]string{resumeID, root})
			} else {
				cfg, err = loadConfig([]string{root})
			}
			if err == nil {
				err = cfg.validate()
			}
			if err == nil {
				runErr := runWith(ctx, cfg, func(runCtx context.Context, svc *session.Service) error {
					host := newGUIHost(svc, renderer)
					return host.ServeReader(runCtx, reader, display)
				})
				if runErr != nil && ctx.Err() == nil {
					_ = json.NewEncoder(display).Encode(map[string]any{"type": "view", "view": map[string]any{"language": "en"}})
					_ = json.NewEncoder(display).Encode(map[string]string{"type": "error", "text": runErr.Error()})
					continue
				}
				return runErr
			}
		}
		if err != nil {
			_ = json.NewEncoder(display).Encode(map[string]string{"type": "error", "text": err.Error()})
		}
	}
}

var errGUICommandTooLarge = errors.New("GUI command exceeds 1 MiB")

func readWelcomeCommand(reader *bufio.Reader) ([]byte, error) {
	line := make([]byte, 0, 4096)
	for {
		part, err := reader.ReadSlice('\n')
		if len(line)+len(part) > 1<<20 {
			for err == bufio.ErrBufferFull {
				_, err = reader.ReadSlice('\n')
			}
			return nil, errGUICommandTooLarge
		}
		line = append(line, part...)
		if err != bufio.ErrBufferFull {
			return line, err
		}
	}
}

func chooseGUISession(ctx context.Context, root string) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", nil
	}
	binary, err := os.Executable()
	if err != nil {
		return "", err
	}
	output, err := exec.CommandContext(ctx, binary, "sessions", "--unfinished-ids", root).Output()
	if err != nil {
		return "", err
	}
	ids := strings.Fields(string(output))
	if len(ids) == 0 {
		return "", nil
	}
	args := []string{"-e", `on run argv`, "-e", `set choices to {"Start a new session"}`, "-e", `repeat with sid in argv`, "-e", `set end of choices to contents of sid`, "-e", `end repeat`, "-e", `set picked to choose from list choices with title "Duo sessions" with prompt "Start a new session or resume an unfinished session."`, "-e", `if picked is false then return "CANCEL"`, "-e", `return item 1 of picked`, "-e", `end run`}
	args = append(args, ids...)
	choice, err := exec.CommandContext(ctx, "osascript", args...).Output()
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(choice))
	if value == "CANCEL" {
		return "", errors.New("session selection cancelled")
	}
	if value == "Start a new session" {
		return "", nil
	}
	return value, nil
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
		absolute, absErr := filepath.Abs(path)
		if absErr != nil {
			return "", absErr
		}
		return absolute, nil
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

	host := newGUIHost(svc, renderer)
	serveErr := host.Serve(ctx, commands, display)
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

func newGUIHost(svc *session.Service, renderer string) *guiipc.Host {
	host := guiipc.NewHost(svc)
	host.SetOpenProject(func(ctx context.Context, selected string) error {
		root, err := workspace.FindRoot(ctx, selected)
		if err != nil {
			return fmt.Errorf("selected folder is not a Git repository: %w", err)
		}
		binary, err := os.Executable()
		if err != nil {
			return err
		}
		next := exec.Command(binary, "gui", "--gui-binary", renderer, root)
		next.Stdout = io.Discard
		stderr := &boundedGUIOutput{}
		next.Stderr = stderr
		if err := next.Start(); err != nil {
			return fmt.Errorf("open project window: %w", err)
		}
		if err := next.Wait(); err != nil {
			return fmt.Errorf("project window failed: %w: %s", err, stderr.String())
		}
		return nil
	})
	host.SetNativeLauncher(func(ctx context.Context, socketPath, agent string) error {
		if runtime.GOOS != "darwin" {
			return fmt.Errorf("native terminal launch is unsupported on %s", runtime.GOOS)
		}
		binary, err := os.Executable()
		if err != nil {
			return err
		}
		command := shellQuote(binary) + " native --socket " + shellQuote(socketPath) + " --agent " + shellQuote(agent)
		script := `tell application "Terminal" to do script "exec ` + strings.ReplaceAll(strings.ReplaceAll(command, `\`, `\\`), `"`, `\"`) + `"`
		launchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		output, err := exec.CommandContext(launchCtx, "osascript", "-e", script).CombinedOutput()
		if err != nil {
			if errors.Is(launchCtx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("open native terminal timed out after 10 seconds; check macOS Automation access for Terminal and whether Terminal is responding (osascript: %v: %s)", err, strings.TrimSpace(string(output)))
			}
			return fmt.Errorf("open native terminal: %w: %s", err, strings.TrimSpace(string(output)))
		}
		return nil
	})
	return host
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

type boundedGUIOutput struct{ buf bytes.Buffer }

func (b *boundedGUIOutput) Write(p []byte) (int, error) {
	const limit = 16 * 1024
	remaining := limit - b.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			_, _ = b.buf.Write(p[:remaining])
		} else {
			_, _ = b.buf.Write(p)
		}
	}
	return len(p), nil
}

func (b *boundedGUIOutput) String() string { return strings.TrimSpace(b.buf.String()) }
