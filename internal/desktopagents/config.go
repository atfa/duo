package desktopagents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/atfa/duo/internal/driver"
)

const configVersion = 1

var safeName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

type Agent struct {
	Name      string `json:"name"`
	CLI       string `json:"cli"`
	Path      string `json:"path,omitempty"`
	Source    string `json:"source,omitempty"`
	Available bool   `json:"available"`
	Error     string `json:"error,omitempty"`
}

type State struct {
	Version   int              `json:"version"`
	Path      string           `json:"path,omitempty"`
	Agents    map[string]Agent `json:"agents"`
	ScanError string           `json:"scan_error,omitempty"`
}

type Registry struct {
	mu       sync.RWMutex
	state    State
	dir      string
	loadErr  string
	prepared bool
}

func Open() (*Registry, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(root, "xduo")
	r := &Registry{dir: dir, state: State{Version: configVersion, Agents: map[string]Agent{}}}
	data, err := os.ReadFile(filepath.Join(dir, "agents.json"))
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &r.state); err != nil {
		r.loadErr = fmt.Sprintf("Could not read agent settings: %v", err)
		return r, nil
	}
	if r.state.Version != configVersion {
		r.loadErr = fmt.Sprintf("Unsupported agent settings version %d", r.state.Version)
		r.state = State{Version: configVersion, Agents: map[string]Agent{}}
	}
	if r.state.Agents == nil {
		r.state.Agents = map[string]Agent{}
	}
	return r, nil
}

func (r *Registry) LoadError() string { r.mu.RLock(); defer r.mu.RUnlock(); return r.loadErr }
func (r *Registry) Exists() bool {
	_, err := os.Stat(filepath.Join(r.dir, "agents.json"))
	return err == nil
}
func (r *Registry) State() State {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := r.state
	out.Agents = make(map[string]Agent, len(r.state.Agents))
	for k, v := range r.state.Agents {
		out.Agents[k] = v
	}
	return out
}
func (r *Registry) ShimDir() string        { return filepath.Join(r.dir, "bin") }
func (r *Registry) NeedsInitialScan() bool { return !r.Exists() }

func (r *Registry) MarkScanError(err error) {
	if err == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state.Version = configVersion
	r.state.ScanError = err.Error()
	if r.state.Agents == nil {
		r.state.Agents = map[string]Agent{}
	}
	_ = r.saveLocked()
}

// PrepareProcessPath is called before the GUI starts any child or session goroutines.
func (r *Registry) PrepareProcessPath() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.prepared {
		return nil
	}
	if err := r.applyShims(r.state); err != nil {
		return err
	}
	os.Setenv("PATH", joinPath(append(append([]string{r.ShimDir()}, filepath.SplitList(r.state.Path)...), filepath.SplitList(os.Getenv("PATH"))...)...))
	r.prepared = true
	return nil
}

// RuntimePath is used after login-shell startup, which may replace PATH.
func (r *Registry) RuntimePath() string {
	return joinPath(append(append([]string{r.ShimDir()}, filepath.SplitList(r.State().Path)...), filepath.SplitList(os.Getenv("PATH"))...)...)
}

func (r *Registry) Refresh(ctx context.Context, names []string) (State, error) {
	path, err := discoverPath(ctx)
	if err != nil {
		fallback := r.cleanPath(joinPath(r.State().Path, os.Getenv("PATH")))
		if fallback == "" {
			return r.State(), err
		}
		state, refreshErr := r.refreshWithPath(ctx, names, fallback)
		if refreshErr == nil {
			r.mu.Lock()
			r.state.ScanError = "Could not read the user shell PATH; used the saved and current PATH: " + err.Error()
			_ = r.saveLocked()
			state.ScanError = r.state.ScanError
			r.mu.Unlock()
		}
		return state, refreshErr
	}
	return r.refreshWithPath(ctx, names, path)
}

func (r *Registry) refreshWithPath(ctx context.Context, names []string, path string) (State, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	path = r.cleanPath(path)
	if path == "" {
		return r.state, errors.New("agent search returned an empty PATH")
	}
	previous := r.state
	next := State{Version: configVersion, Path: path, Agents: map[string]Agent{}}
	for _, name := range names {
		if !safeName.MatchString(name) {
			continue
		}
		old := previous.Agents[name]
		if old.Source == "manual" {
			if executable(old.Path) {
				next.Agents[name] = old
			} else {
				old.Available = false
				old.Error = "Configured executable is missing or not executable"
				next.Agents[name] = old
			}
			continue
		}
		cli := old.CLI
		probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		if plugin, err := driver.Resolve(probeCtx, name, nil, nil); err == nil {
			if manifest, describeErr := plugin.Describe(probeCtx); describeErr == nil && manifest != nil {
				cli = manifest.Agent.CLI
			}
			plugin.Close()
		}
		cancel()
		if cli == "" {
			cli = name
		}
		if !safeName.MatchString(cli) {
			continue
		}
		found := findExecutable(path, cli)
		if found != "" {
			next.Agents[name] = Agent{Name: name, CLI: cli, Path: found, Source: "path"}
			continue
		}
		probeCtx, cancel = context.WithTimeout(ctx, 4*time.Second)
		if plugin, err := driver.Resolve(probeCtx, name, nil, nil); err == nil {
			if result, probeErr := plugin.Probe(probeCtx); probeErr == nil && result != nil && result.Available && executable(result.AgentPath) {
				next.Agents[name] = Agent{Name: name, CLI: cli, Path: result.AgentPath, Source: "detected"}
			}
			plugin.Close()
		}
		cancel()
	}
	next.ScanError = ""
	if err := r.applyShims(next); err != nil {
		_ = r.applyShims(previous)
		return previous, err
	}
	for name, a := range next.Agents {
		if !executable(a.Path) {
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		plugin, probeErr := driver.Resolve(probeCtx, name, nil, nil)
		if probeErr == nil {
			if result, err := plugin.Probe(probeCtx); err == nil && result != nil {
				a.Available, a.Error = result.Available, result.Reason
			} else if err != nil {
				a.Error = err.Error()
			}
			plugin.Close()
		} else {
			a.Error = probeErr.Error()
		}
		cancel()
		next.Agents[name] = a
	}
	r.state = next
	if err := r.saveLocked(); err != nil {
		r.state = previous
		_ = r.applyShims(previous)
		return previous, err
	}
	r.state.ScanError = ""
	r.loadErr = ""
	return next, nil
}

func (r *Registry) Add(ctx context.Context, name, path string, names []string) (State, error) {
	if !safeName.MatchString(name) {
		return r.State(), fmt.Errorf("invalid driver name %q", name)
	}
	known := false
	for _, candidate := range names {
		if name == candidate {
			known = true
			break
		}
	}
	if !known {
		return r.State(), fmt.Errorf("unknown driver %q", name)
	}
	absolute, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return r.State(), err
	}
	if !filepath.IsAbs(absolute) || !executable(absolute) {
		return r.State(), fmt.Errorf("agent path must be an executable file: %s", path)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	previous := r.state
	cli := name
	probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	plugin, err := driver.Resolve(probeCtx, name, nil, nil)
	if err != nil {
		cancel()
		return previous, err
	}
	manifest, err := plugin.Describe(probeCtx)
	plugin.Close()
	cancel()
	if err != nil {
		return previous, err
	}
	if manifest == nil || !safeName.MatchString(manifest.Agent.CLI) {
		return previous, fmt.Errorf("driver %s reported an invalid CLI identity", name)
	}
	cli = manifest.Agent.CLI
	next := previous
	next.Agents = make(map[string]Agent, len(previous.Agents)+1)
	for k, v := range previous.Agents {
		next.Agents[k] = v
	}
	next.Agents[name] = Agent{Name: name, CLI: cli, Path: absolute, Source: "manual"}
	if err := r.applyShims(next); err != nil {
		return previous, err
	}
	probeCtx, cancel = context.WithTimeout(ctx, 4*time.Second)
	plugin, err = driver.Resolve(probeCtx, name, nil, nil)
	if err != nil {
		cancel()
		_ = r.applyShims(previous)
		return previous, err
	}
	result, err := plugin.Probe(probeCtx)
	plugin.Close()
	cancel()
	if err != nil {
		_ = r.applyShims(previous)
		return previous, err
	}
	if result == nil || !result.Available {
		reason := "probe reported unavailable"
		if result != nil && result.Reason != "" {
			reason = result.Reason
		}
		_ = r.applyShims(previous)
		return previous, errors.New(reason)
	}
	a := next.Agents[name]
	a.Available = true
	next.Agents[name] = a
	r.state = next
	if err := r.saveLocked(); err != nil {
		r.state = previous
		_ = r.applyShims(previous)
		return previous, err
	}
	r.loadErr = ""
	return next, nil
}

func (r *Registry) saveLocked() error {
	if err := os.MkdirAll(r.dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(r.state, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(r.dir, ".agents-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(r.dir, "agents.json"))
}

func (r *Registry) applyShims(state State) error {
	if err := os.MkdirAll(r.dir, 0700); err != nil {
		return err
	}
	bin := r.ShimDir()
	if err := os.MkdirAll(bin, 0700); err != nil {
		return err
	}
	// `node` is needed by the common /usr/bin/env node CLI shims installed via npm.
	links := map[string]string{}
	for _, dir := range filepath.SplitList(state.Path) {
		if target := filepath.Join(dir, "node"); executable(target) {
			links["node"] = target
			break
		}
	}
	for _, a := range state.Agents {
		if safeName.MatchString(a.CLI) && executable(a.Path) {
			links[a.CLI] = a.Path
		}
	}
	entries, err := os.ReadDir(bin)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if _, wanted := links[name]; wanted || !safeName.MatchString(name) {
			continue
		}
		path := filepath.Join(bin, name)
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
			if err := os.Remove(path); err != nil {
				return err
			}
		}
	}
	for name, source := range links {
		target := filepath.Join(bin, name)
		if st, err := os.Lstat(target); err == nil {
			if st.Mode()&os.ModeSymlink == 0 {
				return fmt.Errorf("refusing to replace unmanaged file %s", target)
			}
			old, readErr := os.Readlink(target)
			if readErr == nil && filepath.Clean(old) == filepath.Clean(source) {
				continue
			}
		}
		tmp := filepath.Join(bin, ".link-"+name)
		_ = os.Remove(tmp)
		if err := os.Symlink(source, tmp); err != nil {
			return err
		}
		if err := os.Rename(tmp, target); err != nil {
			os.Remove(tmp)
			return err
		}
	}
	return nil
}

func discoverPath(ctx context.Context) (string, error) {
	if runtime.GOOS == "windows" {
		return os.Getenv("PATH"), nil
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		if runtime.GOOS == "darwin" {
			shell = "/bin/zsh"
		} else if executable("/bin/bash") {
			shell = "/bin/bash"
		} else {
			shell = "/bin/sh"
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	args := []string{"-lic", `printf '\n__XDUO_PATH__%s\n' "$PATH"`}
	cmd := exec.CommandContext(ctx, shell, args...)
	// Minimal POSIX shells such as dash do not accept bash-style login flags;
	// invoking them with a hyphenated argv[0] makes -c run as a login shell.
	if shellName := filepath.Base(shell); shellName == "sh" || shellName == "dash" || shellName == "ash" {
		cmd.Args[0] = "-" + shellName
		cmd.Args[1] = "-c"
	}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("read user shell PATH: %w", err)
	}
	lines := strings.Split(string(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], "__XDUO_PATH__") {
			return strings.TrimPrefix(lines[i], "__XDUO_PATH__"), nil
		}
	}
	return "", errors.New("user shell did not return PATH")
}

func cleanPath(path string) string {
	parts := filepath.SplitList(path)
	out := parts[:0]
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || strings.Contains(part, "/Contents/Resources") || strings.Contains(part, "/Contents/MacOS") {
			continue
		}
		out = append(out, part)
	}
	return joinPath(out...)
}

func (r *Registry) cleanPath(path string) string {
	shim := filepath.Clean(r.ShimDir())
	parts := filepath.SplitList(cleanPath(path))
	filtered := parts[:0]
	for _, part := range parts {
		if filepath.Clean(part) != shim {
			filtered = append(filtered, part)
		}
	}
	return joinPath(filtered...)
}
func findExecutable(path, name string) string {
	for _, dir := range filepath.SplitList(path) {
		candidate := filepath.Join(dir, name)
		if executable(candidate) {
			return candidate
		}
	}
	return ""
}
func executable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode().Perm()&0111 != 0
}
func joinPath(paths ...string) string {
	seen := map[string]bool{}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return strings.Join(out, string(os.PathListSeparator))
}
func Names(registered []string) []string {
	out := append([]string{}, registered...)
	sort.Strings(out)
	return out
}
