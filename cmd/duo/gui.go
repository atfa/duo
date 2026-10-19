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
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/atfa/duo/internal/desktopagents"
	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/internal/guiipc"
	"github.com/atfa/duo/internal/session"
	"github.com/atfa/duo/internal/sessionstore"
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
	guiBinaryDefault = "xduo-renderer"
	// guiBinaryEnv names the renderer explicitly, for a build tree that is not on
	// PATH.
	guiBinaryEnv = "DUO_GPUI_BIN"
)

// runGUI starts the welcome renderer or an explicitly selected session.
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
	initialProject := ""
	filtered := args[:0]
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--welcome" {
			welcome = true
		} else if arg == "--initial-project" {
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return errors.New("--initial-project requires a folder path")
			}
			initialProject = args[i+1]
			i++
		} else {
			filtered = append(filtered, arg)
		}
	}
	args = filtered
	registerDrivers()
	registry, err := desktopagents.Open()
	if err != nil {
		return err
	}
	if err := registry.PrepareProcessPath(); err != nil {
		return err
	}
	var foundDrivers []string
	for _, entry := range driver.Discover() {
		foundDrivers = append(foundDrivers, entry.Name)
	}
	driverNames := desktopagents.Names(foundDrivers)
	if welcome {
		if len(args) != 0 {
			return errors.New("duo gui --welcome does not accept session arguments")
		}
		renderer, err := findGUIBinary(binary)
		if err != nil {
			return err
		}
		return serveWelcomeGUI(ctx, renderer, registry, driverNames, initialProject)
	}
	if initialProject != "" {
		return errors.New("--initial-project requires --welcome")
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
	cfg.agentRuntimePath = registry.RuntimePath()
	renderer, err := findGUIBinary(binary)
	if err != nil {
		return err
	}

	return runWith(ctx, cfg, func(ctx context.Context, svc *session.Service) error {
		if err := serveGUI(ctx, svc, renderer, registry, driverNames); err != nil && ctx.Err() == nil {
			log.Printf("Duo GUI: %v", err)
		}
		return nil
	})
}

func serveWelcomeGUI(ctx context.Context, renderer string, registry *desktopagents.Registry, driverNames []string, initialProject string) error {
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
	scanCtx, cancelScans := context.WithCancel(ctx)
	var scanWorkers sync.WaitGroup
	defer func() { cancelScans(); scanWorkers.Wait(); _ = display.Close(); _ = cmd.Wait() }()
	frames := &welcomeFrames{w: display}
	var settingsBusy atomic.Bool
	needsScan := registry.NeedsInitialScan()
	settingsBusy.Store(needsScan)
	var selection *guiipc.ProjectSelection
	pendingRoot := ""
	var selectionMu sync.RWMutex
	getSelection := func() (*guiipc.ProjectSelection, string) {
		selectionMu.RLock()
		defer selectionMu.RUnlock()
		return cloneProjectSelection(selection), pendingRoot
	}
	setSelection := func(next *guiipc.ProjectSelection, root string) {
		selectionMu.Lock()
		selection, pendingRoot = cloneProjectSelection(next), root
		selectionMu.Unlock()
	}
	setSelectionError := func(message string, busy bool) {
		selectionMu.Lock()
		if selection == nil {
			selection = &guiipc.ProjectSelection{}
		}
		selection.Error, selection.Busy = message, busy
		selectionMu.Unlock()
	}
	if initialProject != "" {
		selection, err = inspectProjectSessions(ctx, initialProject)
		if err != nil {
			selection = &guiipc.ProjectSelection{Root: initialProject, Error: err.Error()}
		} else {
			pendingRoot = selection.Root
		}
	}
	setSelection(selection, pendingRoot)
	selection, _ = getSelection()
	initial := welcomeView(registry, driverNames, needsScan, registry.LoadError(), selection)
	if err := frames.write(initial); err != nil {
		return err
	}
	if registry.NeedsInitialScan() {
		scanWorkers.Add(1)
		go func() {
			defer scanWorkers.Done()
			_, scanErr := registry.Refresh(scanCtx, driverNames)
			if scanErr != nil {
				registry.MarkScanError(scanErr)
			}
			current, _ := getSelection()
			_ = frames.write(welcomeView(registry, driverNames, false, scanErrText(scanErr, registry), current))
			settingsBusy.Store(false)
		}()
	}
	for {
		line, err := readWelcomeCommand(reader)
		if errors.Is(err, errGUICommandTooLarge) {
			_ = frames.write(map[string]any{"type": "error", "text": "GUI command exceeds 1 MiB"})
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
		if command.Kind == guiipc.KindRefreshAgents || command.Kind == guiipc.KindAddAgent {
			if !settingsBusy.CompareAndSwap(false, true) {
				_ = frames.write(map[string]any{"type": "error", "text": "Agent settings are already being updated."})
				continue
			}
			current, _ := getSelection()
			_ = frames.write(welcomeView(registry, driverNames, true, "", current))
			scanWorkers.Add(1)
			go func(command guiipc.Command) {
				defer scanWorkers.Done()
				var operationErr error
				if command.Kind == guiipc.KindRefreshAgents {
					_, operationErr = registry.Refresh(scanCtx, driverNames)
				} else {
					_, operationErr = registry.Add(scanCtx, command.Driver, command.Path, driverNames)
				}
				current, _ := getSelection()
				_ = frames.write(welcomeView(registry, driverNames, false, scanErrText(operationErr, registry), current))
				settingsBusy.Store(false)
			}(command)
			continue
		}
		if command.Kind == guiipc.KindBackToWelcome {
			setSelection(nil, "")
			_ = frames.write(welcomeView(registry, driverNames, false, "", nil))
			continue
		}
		if command.Kind == guiipc.KindOpenProject {
			setSelection(&guiipc.ProjectSelection{Root: command.Path, Busy: true}, "")
			current, _ := getSelection()
			_ = frames.write(welcomeView(registry, driverNames, false, "", current))
			var selectErr error
			current, selectErr = inspectProjectSessions(ctx, command.Path)
			if selectErr != nil {
				current = &guiipc.ProjectSelection{Root: command.Path, Error: selectErr.Error()}
				setSelection(current, "")
			} else {
				setSelection(current, current.Root)
			}
			current, _ = getSelection()
			_ = frames.write(welcomeView(registry, driverNames, false, "", current))
			continue
		}
		if command.Kind != guiipc.KindStartSession {
			continue
		}
		if settingsBusy.Load() {
			setSelectionError("Agent settings are still being updated. Please try again.", false)
			current, _ := getSelection()
			_ = frames.write(welcomeView(registry, driverNames, false, "", current))
			continue
		}
		selection, pendingRoot := getSelection()
		if selection != nil {
			selection.Busy, selection.Error = true, ""
		}
		setSelection(selection, pendingRoot)
		_ = frames.write(welcomeView(registry, driverNames, false, "", selection))
		root, err := validatePendingProject(ctx, pendingRoot, selection, command.Path)
		resumeID := command.SessionID
		if err == nil && resumeID != "" {
			err = validateResumeChoice(root, resumeID)
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
				cfg.agentRuntimePath = registry.RuntimePath()
				runErr := runWith(ctx, cfg, func(runCtx context.Context, svc *session.Service) error {
					host := newGUIHost(svc, renderer, registry, driverNames)
					return host.ServeReader(runCtx, reader, display)
				})
				if runErr != nil && ctx.Err() == nil {
					setSelectionError(runErr.Error(), false)
					current, _ := getSelection()
					_ = frames.write(welcomeView(registry, driverNames, false, "", current))
					continue
				}
				return runErr
			}
		}
		if err != nil {
			setSelectionError(err.Error(), false)
			current, _ := getSelection()
			_ = frames.write(welcomeView(registry, driverNames, false, "", current))
		}
	}
}

type welcomeFrames struct {
	mu sync.Mutex
	w  io.Writer
}

func (f *welcomeFrames) write(value any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return json.NewEncoder(f.w).Encode(value)
}

func welcomeView(registry *desktopagents.Registry, driverNames []string, busy bool, message string, selection *guiipc.ProjectSelection) map[string]any {
	state := registry.State()
	if message == "" {
		message = state.ScanError
	}
	agents := make([]guiipc.DesktopAgent, 0, len(state.Agents))
	available := make([]string, 0, len(state.Agents))
	for _, a := range state.Agents {
		agents = append(agents, guiipc.DesktopAgent{Name: a.Name, CLI: a.CLI, Path: a.Path, Source: a.Source, Available: a.Available, Error: a.Error})
		if a.Available {
			available = append(available, a.Name)
		}
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i].Name < agents[j].Name })
	sort.Strings(available)
	return map[string]any{"type": "view", "view": map[string]any{"language": "en", "desktop_agents": agents, "available_drivers": available, "agent_driver_names": driverNames, "agent_scan_error": message, "agent_scan_busy": busy, "project_selection": selection}}
}

func scanErrText(err error, registry *desktopagents.Registry) string {
	if err != nil {
		return err.Error()
	}
	return registry.LoadError()
}

func cloneProjectSelection(selection *guiipc.ProjectSelection) *guiipc.ProjectSelection {
	if selection == nil {
		return nil
	}
	clone := *selection
	clone.Sessions = append([]guiipc.SessionChoice(nil), selection.Sessions...)
	return &clone
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

func inspectProjectSessions(ctx context.Context, selected string) (*guiipc.ProjectSelection, error) {
	root, err := workspace.FindRoot(ctx, selected)
	if err != nil {
		return nil, fmt.Errorf("selected folder is not a Git repository: %w", err)
	}
	baseDir, err := sessionstore.DefaultBaseDir()
	if err != nil {
		return nil, err
	}
	items, err := sessionstore.List(baseDir, sessionstore.RepoID(root))
	if err != nil {
		return nil, err
	}
	selection := &guiipc.ProjectSelection{Root: root}
	for _, item := range items {
		if !safeSessionIDLine(item.SessionID) {
			selection.Sessions = append(selection.Sessions, guiipc.SessionChoice{SessionID: item.SessionID, Error: "Invalid session ID; this record cannot be resumed."})
			continue
		}
		choice := guiipc.SessionChoice{SessionID: item.SessionID, Mode: string(item.Snapshot.Mode), Phase: string(item.Snapshot.Phase)}
		if !item.Snapshot.UpdatedAt.IsZero() {
			choice.UpdatedAt = item.Snapshot.UpdatedAt.Local().Format(time.RFC3339)
		}
		if item.Err != nil {
			choice.Error = "Could not read saved session: " + item.Err.Error()
			selection.Sessions = append(selection.Sessions, choice)
			continue
		}
		if !item.Unfinished() {
			continue
		}
		choice.Active = sessionIsActive(item.Dir)
		selection.Sessions = append(selection.Sessions, choice)
	}
	return selection, nil
}

func validatePendingProject(ctx context.Context, pendingRoot string, selection *guiipc.ProjectSelection, requestedRoot string) (string, error) {
	if selection == nil || pendingRoot == "" || requestedRoot != pendingRoot || selection.Root != pendingRoot {
		return "", errors.New("choose a project folder before starting a session")
	}
	root, err := workspace.FindRoot(ctx, requestedRoot)
	if err != nil {
		return "", err
	}
	if root != pendingRoot {
		return "", errors.New("selected project changed; choose it again")
	}
	return root, nil
}

func validateResumeChoice(root, sessionID string) error {
	if !safeSessionIDLine(sessionID) {
		return errors.New("invalid session ID")
	}
	baseDir, err := sessionstore.DefaultBaseDir()
	if err != nil {
		return err
	}
	items, err := sessionstore.List(baseDir, sessionstore.RepoID(root))
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.SessionID == sessionID && item.Err == nil && item.Unfinished() && !sessionIsActive(item.Dir) {
			return nil
		}
	}
	return errors.New("that session is no longer available to resume; refresh the project selection")
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
func serveGUI(ctx context.Context, svc *session.Service, renderer string, registry *desktopagents.Registry, driverNames []string) error {
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

	host := newGUIHost(svc, renderer, registry, driverNames)
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

func newGUIHost(svc *session.Service, renderer string, registry *desktopagents.Registry, driverNames []string) *guiipc.Host {
	host := guiipc.NewHost(svc)
	host.SetDesktopAgents(registry, driverNames)
	host.SetOpenProject(func(ctx context.Context, selected string) error {
		root, err := workspace.FindRoot(ctx, selected)
		if err != nil {
			return fmt.Errorf("selected folder is not a Git repository: %w", err)
		}
		binary, err := os.Executable()
		if err != nil {
			return err
		}
		next := exec.Command(binary, "gui", "--welcome", "--initial-project", root, "--gui-binary", renderer)
		next.Stdout = io.Discard
		stderr := &boundedGUIOutput{}
		next.Stderr = stderr
		if err := next.Start(); err != nil {
			return fmt.Errorf("open project window: %w", err)
		}
		go func() {
			if err := next.Wait(); err != nil {
				log.Printf("project selection window failed: %v: %s", err, stderr.String())
			}
		}()
		return nil
	})
	return host
}

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
