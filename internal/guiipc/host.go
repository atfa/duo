package guiipc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/atfa/duo/internal/desktopagents"
	"github.com/atfa/duo/internal/models"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/session"
	"github.com/atfa/duo/internal/sessionstore"
)

// Session is the seam the GUI host reads. It is exactly the surface the terminal
// interface uses: a projection, a stream, the human verbs, and the two reads a
// window needs on top. Naming it here rather than depending on *session.Service
// is what lets the host be driven by a fake in a test.
//
// A frontend that can call these methods still cannot start, stop, steer or sign
// a session beyond the human verbs in session.Command: there is no gate verb on
// this interface, and the compiler is what keeps it that way.
type Session interface {
	// View is the complete projection at one moment.
	View() session.View
	// Subscribe returns the view plus every event after it.
	Subscribe(buffer int) (session.Subscription, func())
	// Do performs one human verb.
	Do(ctx context.Context, cmd session.Command) error
	// Models reads the driver's published catalog. Host calls it asynchronously.
	Models(ctx context.Context, agent protocol.AgentID) ([]models.Model, error)
	// StatusText renders the workflow status line.
	StatusText(ctx context.Context) string
	// History returns the interface entries recorded before a resume.
	History() []sessionstore.TUIEntry
}

type nativeSession interface {
	NativeRunning(protocol.AgentID) bool
	Attach(protocol.AgentID, io.Writer) ([]byte, error)
	Detach(protocol.AgentID)
	Write(protocol.AgentID, []byte) error
	Resize(int, int) error
	ResizeAgent(protocol.AgentID, int, int) error
}

const (
	// defaultHeartbeat is how often a full view is re-sent. Status and agent
	// activity change without an event of their own, so the view is the only way
	// the window can follow them.
	defaultHeartbeat = 500 * time.Millisecond
	// defaultBuffer matches the bus's own default. A slow renderer drops events
	// rather than blocking the session, and the next view repairs the display.
	defaultBuffer = 64
	// diffstatTTL avoids running Git for every 500 ms view heartbeat.
	diffstatTTL = 3 * time.Second
	// maxDiffPreviewBytes keeps a malicious or generated patch from inflating
	// every GUI view or consuming unbounded host memory.
	maxDiffPreviewBytes = 12 * 1024
	// maxCommandBytes bounds one command line so a frontend that never sends a
	// newline cannot make the host buffer without limit.
	maxCommandBytes = 1 << 20
	// PTY bytes are framed and base64-encoded in NDJSON. Keep each chunk short
	// enough that even its encoded form remains a small protocol line.
	terminalOutputChunkBytes = 16 * 1024
	terminalOutputQueueDepth = 64
	terminalReplayMaxBytes   = 1 << 20
	terminalInputMaxBytes    = 64 * 1024
	terminalMaxDimension     = 512
)

// Host serves the frozen NDJSON protocol over a Gpio pair, driving one session.
//
// It is frontend-neutral in the only sense that matters: it translates the same
// View and the same Commands the terminal interface uses, and it invents no
// workflow of its own. Everything it sends is derived from a session read, and
// everything it accepts is a session.Command.
type Host struct {
	s            Session
	openProject  func(context.Context, string) error
	launchNative func(context.Context, string, string) error
	nativeMu     sync.Mutex
	native       map[protocol.AgentID]*nativeAttachment

	// heartbeat and buffer are the defaults above, overridden only by tests.
	heartbeat     time.Duration
	buffer        int
	drivers       []string
	catalogMu     sync.RWMutex
	desktop       *desktopagents.Registry
	driverNames   []string
	settingsMu    sync.RWMutex
	settingsBusy  bool
	settingsError string
	catalogs      map[protocol.AgentID]catalogResult
	diffMu        sync.Mutex
	diffstats     map[protocol.AgentID]diffstatResult
}

type nativeAttachment struct {
	mu        sync.Mutex
	closeOnce sync.Once
	listener  net.Listener
	conn      net.Conn
	stream    *nativeFrameWriter
	terminal  bool
	output    *terminalOutputWriter
	cancel    context.CancelFunc
	done      chan struct{}
	attached  bool
	closed    bool
}

type terminalOutputWriter struct {
	ctx    context.Context
	chunks chan []byte
	failed chan error
	mu     sync.Mutex
	err    error
}

var errTerminalOutputBacklog = errors.New("embedded terminal output backlog exceeded; terminal detached")

type catalogResult struct {
	models []Model
	loaded bool
	err    string
}

type diffstatResult struct {
	path, base string
	value      Diffstat
	preview    DiffPreview
	updated    time.Time
	loading    bool
}

// NewHost returns a host for one session.
func NewHost(s Session) *Host {
	h := &Host{s: s, heartbeat: defaultHeartbeat, buffer: defaultBuffer, catalogs: make(map[protocol.AgentID]catalogResult), diffstats: make(map[protocol.AgentID]diffstatResult)}
	return h
}

func (h *Host) SetOpenProject(fn func(context.Context, string) error) { h.openProject = fn }
func (h *Host) SetNativeLauncher(fn func(context.Context, string, string) error) {
	h.launchNative = fn
}
func (h *Host) SetDesktopAgents(registry *desktopagents.Registry, names []string) {
	h.desktop = registry
	h.driverNames = append([]string{}, names...)
	h.settingsError = registry.LoadError()
}

// Serve speaks the protocol until the frontend goes away, the session closes its
// event stream, or ctx is cancelled.
//
// in and out are the GUI's command stream and the GUI's display stream, so a
// caller that has spawned a renderer subprocess passes the child's stdout as in
// and the child's stdin as out. Nothing else may be written to out: the GUI's
// decoder treats every line as a protocol frame.
func (h *Host) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	return h.ServeReader(ctx, bufio.NewReader(in), out)
}

// ServeReader continues a command stream using its existing buffer. This is used
// by the welcome flow, which must hand the same reader to the session host after
// the renderer has selected a project.
func (h *Host) ServeReader(ctx context.Context, in *bufio.Reader, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		h.closeNatives()
	}()

	sub, unsubscribe := h.s.Subscribe(h.buffer)
	defer unsubscribe()

	sink := &frameSink{w: out}
	h.updateDesktopView()

	// The view comes first so the window has headers before the backlog lands.
	if err := sink.view(h.projectView(ctx, sub.View, h.s.StatusText(ctx))); err != nil {
		return err
	}
	// Then the conversation that led here. The store keeps it per pane rather
	// than per event; fromHistory is the one place that translation happens.
	for _, entry := range h.s.History() {
		if err := sink.event(fromHistory(entry)); err != nil {
			return err
		}
	}
	for _, id := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		go h.loadCatalog(ctx, id)
	}
	done := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(done) }) }

	go h.readCommands(ctx, in, sink, stop)

	// Live events: each one, then a fresh view, so one dropped event can never
	// leave a stale field on screen.
	go func() {
		defer stop()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-sub.Events:
				if !ok {
					// The session closed its bus. Say so before Serve returns, so the
					// window can stop waiting for a view that will never come.
					_ = sink.closed("session ended")
					return
				}
				if err := sink.event(fromEvent(ev)); err != nil {
					return
				}
				if err := h.sendView(ctx, sink); err != nil {
					return
				}
			}
		}
	}()

	go func() {
		defer stop()
		ticker := time.NewTicker(h.heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := h.sendView(ctx, sink); err != nil {
					return
				}
			}
		}
	}()

	select {
	case <-done:
	case <-ctx.Done():
	}

	// A write failure is the only failure this host owns: a malformed command is
	// the frontend's bug and was already reported, and a closed session is a
	// normal end.
	if err := sink.err(); err != nil {
		return err
	}
	return ctx.Err()
}

// sendView projects and sends one view. It is the only place a view is built, so
// the initial frame, the post-event frame and the heartbeat cannot disagree.
func (h *Host) sendView(ctx context.Context, sink *frameSink) error {
	return sink.view(h.projectView(ctx, h.s.View(), h.s.StatusText(ctx)))
}

func (h *Host) projectView(ctx context.Context, sv session.View, status string) View {
	v := fromView(sv, status)
	h.catalogMu.RLock()
	v.AvailableDrivers = append([]string{}, h.drivers...)
	for id, result := range h.catalogs {
		a := v.Agents[wireAgent(id)]
		a.Models, a.ModelsLoaded, a.ModelsError = append([]Model{}, result.models...), result.loaded, result.err
		v.Agents[wireAgent(id)] = a
	}
	h.catalogMu.RUnlock()
	h.settingsMu.RLock()
	v.AgentScanBusy, v.AgentScanError = h.settingsBusy, h.settingsError
	h.settingsMu.RUnlock()
	if h.desktop != nil {
		state := h.desktop.State()
		v.AgentDriverNames = append([]string{}, h.driverNames...)
		if v.AgentScanError == "" {
			v.AgentScanError = state.ScanError
		}
		for _, a := range state.Agents {
			v.DesktopAgents = append(v.DesktopAgents, DesktopAgent{Name: a.Name, CLI: a.CLI, Path: a.Path, Source: a.Source, Available: a.Available, Error: a.Error})
		}
		sort.Slice(v.DesktopAgents, func(i, j int) bool { return v.DesktopAgents[i].Name < v.DesktopAgents[j].Name })
	}
	h.nativeMu.Lock()
	for _, id := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		a := v.Agents[wireAgent(id)]
		a.NativeOpen = h.native[id] != nil
		v.Agents[wireAgent(id)] = a
	}
	h.nativeMu.Unlock()
	for _, id := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		a := v.Agents[wireAgent(id)]
		a.Diffstat, a.DiffPreview = h.worktreeEvidence(ctx, id, a.WorktreePath, sv.Worktrees.BaseCommit)
		v.Agents[wireAgent(id)] = a
	}
	return v
}

// worktreeDiffstat returns the latest cached summary and refreshes it in the
// background when stale. Git work can take seconds on a large tree; never hold up
// a protocol heartbeat or event view to wait for it.
func (h *Host) worktreeEvidence(ctx context.Context, id protocol.AgentID, path, base string) (Diffstat, DiffPreview) {
	if path == "" {
		return Diffstat{State: "unavailable"}, DiffPreview{State: "unavailable"}
	}
	now := time.Now()
	h.diffMu.Lock()
	entry := h.diffstats[id]
	if entry.path != path || entry.base != base {
		entry = diffstatResult{path: path, base: base, value: Diffstat{State: "loading"}, preview: DiffPreview{State: "loading"}}
	}
	if !entry.loading && (entry.updated.IsZero() || now.Sub(entry.updated) >= diffstatTTL) {
		entry.loading = true
		entry.value.State = "loading"
		entry.preview.State = "loading"
		h.diffstats[id] = entry
		go h.refreshDiffstat(ctx, id, path, base)
	} else {
		h.diffstats[id] = entry
	}
	value := entry.value
	h.diffMu.Unlock()
	return value, entry.preview
}

func (h *Host) refreshDiffstat(parent context.Context, id protocol.AgentID, path, base string) {
	ctx, cancel := context.WithTimeout(parent, 2500*time.Millisecond)
	defer cancel()
	value, preview := readWorktreeEvidence(ctx, path, base)
	h.diffMu.Lock()
	defer h.diffMu.Unlock()
	entry := h.diffstats[id]
	if entry.path != path || entry.base != base {
		return
	}
	entry.value, entry.preview, entry.updated, entry.loading = value, preview, time.Now(), false
	h.diffstats[id] = entry
}

func readDiffstat(ctx context.Context, path, base string) Diffstat {
	stat, _ := readWorktreeEvidence(ctx, path, base)
	return stat
}

func readWorktreeEvidence(ctx context.Context, path, base string) (Diffstat, DiffPreview) {
	// A repository can configure core.fsmonitor to run an arbitrary command.
	// Diffstat runs automatically on GUI heartbeats, so pin this setting off for
	// every Git invocation rather than trusting the worktree's local config.
	args := []string{
		"-c", "core.fsmonitor=false", "-C", path, "diff", "--numstat",
		"--no-ext-diff", "--no-textconv", "--no-renames",
	}
	if base == "" {
		args = append(args, "HEAD")
	} else {
		args = append(args, base)
	}
	out, err := exec.CommandContext(ctx, "git", args...).Output()
	if err != nil {
		failure := truncateRunes(err.Error(), 300)
		return Diffstat{State: "error", Error: failure}, DiffPreview{State: "error", Error: failure}
	}
	stat := Diffstat{State: "ready"}
	for _, line := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			continue
		}
		stat.Files++
		if n, err := strconv.Atoi(fields[0]); err == nil {
			stat.Insertions += n
		}
		if n, err := strconv.Atoi(fields[1]); err == nil {
			stat.Deletions += n
		}
	}
	statusCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	status, err := exec.CommandContext(statusCtx, "git", "-c", "core.fsmonitor=false", "-C", path, "status", "--porcelain", "--untracked-files=normal", "-z").Output()
	if err != nil {
		failure := truncateRunes(err.Error(), 300)
		return Diffstat{State: "error", Error: failure}, DiffPreview{State: "error", Error: failure}
	}
	for _, record := range strings.Split(string(status), "\x00") {
		if strings.HasPrefix(record, "?? ") {
			stat.Untracked++
		}
	}
	previewCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	args = []string{"-c", "core.fsmonitor=false", "-C", path, "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--no-color", "--no-prefix"}
	if base == "" {
		args = append(args, "HEAD")
	} else {
		args = append(args, base)
	}
	args = append(args, "--")
	buffer := &boundedBuffer{limit: maxDiffPreviewBytes}
	cmd := exec.CommandContext(previewCtx, "git", args...)
	cmd.Stdout = buffer
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		failure := truncateRunes(err.Error(), 300)
		return stat, DiffPreview{State: "error", Error: failure}
	}
	return stat, DiffPreview{State: "ready", Text: buffer.String(), Truncated: buffer.truncated}
}

// boundedBuffer retains only the first limit bytes while continuing to drain
// child output, so large diffs cannot grow memory or block the child on a full
// pipe. UTF-8 cleanup happens when the preview is serialized below.
type boundedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			_, _ = b.buf.Write(p[:remaining])
			b.truncated = true
		} else {
			_, _ = b.buf.Write(p)
		}
	} else if len(p) > 0 {
		b.truncated = true
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	return strings.ToValidUTF8(b.buf.String(), "�")
}

func (h *Host) loadCatalog(ctx context.Context, id protocol.AgentID) {
	driverName := h.s.View().Agents[id].Driver
	models, err := h.s.Models(ctx, id)
	if h.s.View().Agents[id].Driver != driverName {
		return
	}
	result := catalogResult{models: fromModels(models), loaded: true}
	if err != nil {
		result.err = err.Error()
	}
	h.catalogMu.Lock()
	if h.s.View().Agents[id].Driver == driverName {
		h.catalogs[id] = result
	}
	h.catalogMu.Unlock()
}

// readCommands is the only writer to the session. It runs until the command
// stream ends, which is what a frontend process exiting looks like: the session
// then has no one left to render it, so Serve returns.
func (h *Host) readCommands(ctx context.Context, in *bufio.Reader, sink *frameSink, stop func()) {
	defer stop()
	for {
		line, err := readGUICommand(in)
		if err != nil {
			return
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var envelope Command
		if json.Unmarshal(line, &envelope) == nil && envelope.Type == typeCommand {
			if envelope.Kind == KindRefreshAgents || envelope.Kind == KindAddAgent {
				h.handleDesktopSettings(ctx, envelope, sink)
				continue
			}
			if envelope.Kind == KindOpenNative || envelope.Kind == KindCloseNative {
				agent, err := parseAgent(envelope.Agent)
				if err != nil {
					_ = sink.failure(err)
				} else if envelope.Kind == KindCloseNative {
					h.closeNative(agent)
					_ = h.sendView(ctx, sink)
				} else {
					go func() {
						err := h.openNative(ctx, agent, sink)
						if err != nil && !errors.Is(err, context.Canceled) {
							_ = sink.failure(err)
						}
						_ = h.sendView(ctx, sink)
					}()
				}
				continue
			}
			if isTerminalCommand(envelope.Kind) {
				h.handleTerminalCommand(ctx, envelope, sink)
				continue
			}
			if envelope.Kind == KindOpenProject {
				if h.openProject == nil {
					_ = sink.failure(errors.New("project picker is unavailable"))
				} else if strings.TrimSpace(envelope.Path) == "" {
					_ = sink.failure(errors.New("no project folder was selected"))
				} else {
					go func(path string) {
						if err := h.openProject(ctx, path); err != nil {
							_ = sink.failure(err)
						}
					}(envelope.Path)
				}
				continue
			}
		}
		cmd, quit, err := decodeCommand(line)
		if err != nil {
			// Report and keep going: one bad frame must not end the window.
			_ = sink.failure(err)
			continue
		}
		if quit {
			return
		}
		if changed, ok := cmd.(session.SetDriver); ok {
			h.closeNative(changed.Agent)
		}
		if err := h.s.Do(ctx, cmd); err != nil {
			_ = sink.failure(err)
		} else if changed, ok := cmd.(session.SetDriver); ok {
			h.catalogMu.Lock()
			delete(h.catalogs, changed.Agent)
			h.catalogMu.Unlock()
			go h.loadCatalog(ctx, changed.Agent)
		}
	}
}

func isTerminalCommand(kind string) bool {
	switch kind {
	case KindTerminalOpen, KindTerminalClose, KindTerminalInput, KindTerminalResize:
		return true
	default:
		return false
	}
}

func (h *Host) handleTerminalCommand(ctx context.Context, command Command, sink *frameSink) {
	id, err := parseAgent(command.Agent)
	if err != nil {
		_ = sink.failure(err)
		return
	}
	switch command.Kind {
	case KindTerminalOpen:
		go func() {
			err := h.openTerminal(ctx, id, sink)
			if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, errTerminalOutputBacklog) {
				_ = sink.failure(err)
			}
			_ = h.sendView(ctx, sink)
		}()
	case KindTerminalClose:
		h.closeTerminal(id)
		_ = h.sendView(ctx, sink)
	case KindTerminalInput:
		data, err := decodeTerminalInput(command.Data)
		if err != nil {
			_ = sink.failure(err)
			return
		}
		if err := h.writeTerminalInput(id, data); err != nil {
			h.failTerminal(ctx, id, err, sink)
		}
	case KindTerminalResize:
		if command.Cols < 1 || command.Cols > terminalMaxDimension || command.Rows < 1 || command.Rows > terminalMaxDimension {
			_ = sink.failure(fmt.Errorf("terminal size must be between 1 and %d columns and rows", terminalMaxDimension))
			return
		}
		if err := h.resizeTerminal(id, command.Cols, command.Rows); err != nil {
			h.failTerminal(ctx, id, err, sink)
		}
	}
}

func decodeTerminalInput(encoded string) ([]byte, error) {
	if len(encoded) > base64.StdEncoding.EncodedLen(terminalInputMaxBytes) {
		return nil, errors.New("terminal input exceeds 64 KiB")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("terminal input is not valid base64: %w", err)
	}
	if len(data) > terminalInputMaxBytes {
		return nil, errors.New("terminal input exceeds 64 KiB")
	}
	return data, nil
}

func (h *Host) updateDesktopView() {
	if h.desktop == nil {
		return
	}
	var available []string
	for name, a := range h.desktop.State().Agents {
		if a.Available {
			available = append(available, name)
		}
	}
	sort.Strings(available)
	h.catalogMu.Lock()
	h.drivers = available
	h.catalogMu.Unlock()
}

func (h *Host) handleDesktopSettings(ctx context.Context, cmd Command, sink *frameSink) {
	if h.desktop == nil {
		_ = sink.failure(errors.New("agent settings are unavailable"))
		return
	}
	h.settingsMu.Lock()
	if h.settingsBusy {
		h.settingsMu.Unlock()
		_ = sink.failure(errors.New("agent settings are already being updated"))
		return
	}
	h.settingsBusy = true
	h.settingsError = ""
	h.settingsMu.Unlock()
	_ = h.sendView(ctx, sink)
	go func() {
		var err error
		if cmd.Kind == KindRefreshAgents {
			_, err = h.desktop.Refresh(ctx, h.driverNames)
		} else {
			_, err = h.desktop.Add(ctx, cmd.Driver, cmd.Path, h.driverNames)
		}
		h.settingsMu.Lock()
		h.settingsBusy = false
		if err != nil {
			h.settingsError = err.Error()
		}
		h.settingsMu.Unlock()
		h.updateDesktopView()
		_ = h.sendView(ctx, sink)
	}()
}

func readGUICommand(reader *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		part, err := reader.ReadSlice('\n')
		if len(line)+len(part) > maxCommandBytes {
			if err == bufio.ErrBufferFull {
				for err == bufio.ErrBufferFull {
					_, err = reader.ReadSlice('\n')
				}
			}
			return nil, fmt.Errorf("GUI command exceeds %d bytes", maxCommandBytes)
		}
		line = append(line, part...)
		if err == nil {
			return line, nil
		}
		if err != bufio.ErrBufferFull {
			return nil, err
		}
	}
}

func (h *Host) openNative(ctx context.Context, id protocol.AgentID, sink *frameSink) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := h.s.(nativeSession); !ok {
		return errors.New("native attach is unavailable")
	}
	if h.launchNative == nil {
		return errors.New("native terminal launcher is unavailable")
	}
	if !h.s.(nativeSession).NativeRunning(id) {
		return fmt.Errorf("%s agent process has exited; restart it before opening its native terminal", id)
	}
	dir, err := os.MkdirTemp("", "duo-native-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, "attach.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	nctx, cancel := context.WithCancel(ctx)
	link := &nativeAttachment{listener: listener, cancel: cancel, done: make(chan struct{})}
	h.nativeMu.Lock()
	if h.native == nil {
		h.native = make(map[protocol.AgentID]*nativeAttachment)
	}
	if h.native[id] != nil {
		h.nativeMu.Unlock()
		cancel()
		listener.Close()
		return fmt.Errorf("%s already has a native terminal open", id)
	}
	if err := nctx.Err(); err != nil {
		h.nativeMu.Unlock()
		cancel()
		listener.Close()
		return err
	}
	h.native[id] = link
	h.nativeMu.Unlock()
	_ = h.sendView(ctx, sink)
	defer h.finishNative(id, link)
	go func() {
		<-nctx.Done()
		h.closeNativeTransport(link)
	}()
	if err := h.launchNative(nctx, path, string(wireAgent(id))); err != nil {
		if nctx.Err() != nil {
			return nctx.Err()
		}
		return err
	}
	if ul, ok := listener.(*net.UnixListener); ok {
		_ = ul.SetDeadline(time.Now().Add(30 * time.Second))
	}
	conn, err := listener.Accept()
	if err != nil {
		if nctx.Err() != nil {
			return nctx.Err()
		}
		return fmt.Errorf("native terminal did not connect: %w", err)
	}
	h.nativeMu.Lock()
	if h.native[id] != link {
		h.nativeMu.Unlock()
		_ = conn.Close()
		return context.Canceled
	}
	link.mu.Lock()
	if link.closed || nctx.Err() != nil {
		link.mu.Unlock()
		h.nativeMu.Unlock()
		_ = conn.Close()
		return context.Canceled
	}
	link.conn = conn
	ns := h.s.(nativeSession)
	stream := &nativeFrameWriter{conn: conn}
	link.stream = stream
	replayWriter := &nativeReplayWriter{stream: stream, ready: make(chan struct{})}
	recent, err := ns.Attach(id, replayWriter)
	if err == nil {
		link.attached = true
	}
	link.mu.Unlock()
	h.nativeMu.Unlock()
	if err != nil {
		replayWriter.release()
		_ = stream.WriteFrame('E', []byte(err.Error()))
		return err
	}
	if len(recent) > 0 {
		if err := stream.WriteFrame('O', recent); err != nil {
			replayWriter.release()
			return err
		}
	}
	replayWriter.release()
	for {
		kind, payload, err := readNativeFrame(conn)
		if err != nil {
			if nctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("native terminal disconnected: %w", err)
		}
		switch kind {
		case 'D':
			if err := ns.Write(id, payload); err != nil {
				_ = stream.WriteFrame('E', []byte(err.Error()))
				return err
			}
		case 'R':
			if len(payload) != 8 {
				err := errors.New("invalid native resize frame")
				_ = stream.WriteFrame('E', []byte(err.Error()))
				return err
			}
			cols, rows := int(binary.BigEndian.Uint32(payload[:4])), int(binary.BigEndian.Uint32(payload[4:]))
			if cols < 1 || cols > 65535 || rows < 1 || rows > 65535 {
				err := errors.New("native terminal size is invalid")
				_ = stream.WriteFrame('E', []byte(err.Error()))
				return err
			}
			if err := ns.Resize(cols, rows); err != nil {
				_ = stream.WriteFrame('E', []byte(err.Error()))
				return err
			}
		case 'Q':
			return nil
		default:
			err := fmt.Errorf("invalid native frame %q", kind)
			_ = stream.WriteFrame('E', []byte(err.Error()))
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
	}
}

func (h *Host) openTerminal(ctx context.Context, id protocol.AgentID, sink *frameSink) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ns, ok := h.s.(nativeSession)
	if !ok {
		return errors.New("terminal attach is unavailable")
	}
	if !ns.NativeRunning(id) {
		return fmt.Errorf("%s agent process has exited; restart it before opening its terminal", id)
	}
	nctx, cancel := context.WithCancel(ctx)
	output := &terminalOutputWriter{
		ctx:    nctx,
		chunks: make(chan []byte, terminalOutputQueueDepth),
		failed: make(chan error, 1),
	}
	link := &nativeAttachment{terminal: true, output: output, cancel: cancel, done: make(chan struct{})}
	h.nativeMu.Lock()
	if h.native == nil {
		h.native = make(map[protocol.AgentID]*nativeAttachment)
	}
	if h.native[id] != nil {
		h.nativeMu.Unlock()
		cancel()
		return fmt.Errorf("%s already has a native terminal open", id)
	}
	if err := nctx.Err(); err != nil {
		h.nativeMu.Unlock()
		cancel()
		return err
	}
	h.native[id] = link
	h.nativeMu.Unlock()
	_ = h.sendView(ctx, sink)
	defer h.finishNative(id, link)
	go func() {
		<-nctx.Done()
		h.closeNativeTransport(link)
	}()

	// Attach while holding the same locks used by closeNativeTransport's map
	// lookup so a close cannot detach before the writer has been installed.
	h.nativeMu.Lock()
	if h.native[id] != link {
		h.nativeMu.Unlock()
		return context.Canceled
	}
	link.mu.Lock()
	if link.closed || nctx.Err() != nil {
		link.mu.Unlock()
		h.nativeMu.Unlock()
		return context.Canceled
	}
	recent, err := ns.Attach(id, output)
	if err == nil {
		link.attached = true
	}
	link.mu.Unlock()
	h.nativeMu.Unlock()
	if err != nil {
		return err
	}
	if len(recent) > terminalReplayMaxBytes {
		return fmt.Errorf("terminal replay exceeds %d bytes", terminalReplayMaxBytes)
	}
	if err := sendTerminalOutput(sink, id, recent); err != nil {
		return err
	}
	return h.relayTerminalOutput(nctx, id, output, sink)
}

func sendTerminalOutput(sink *frameSink, id protocol.AgentID, data []byte) error {
	for len(data) > 0 {
		size := min(len(data), terminalOutputChunkBytes)
		if err := sink.terminalOutput(id, data[:size]); err != nil {
			return err
		}
		data = data[size:]
	}
	return nil
}

func (h *Host) relayTerminalOutput(ctx context.Context, id protocol.AgentID, output *terminalOutputWriter, sink *frameSink) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case chunk := <-output.chunks:
			if err := sink.terminalOutput(id, chunk); err != nil {
				return err
			}
		case err := <-output.failed:
			// Preserve every complete queued chunk before reporting a terminal
			// failure. The writer stops accepting bytes once it signals failure.
			for {
				select {
				case chunk := <-output.chunks:
					if writeErr := sink.terminalOutput(id, chunk); writeErr != nil {
						return writeErr
					}
				default:
					if writeErr := sink.terminalError(id, err.Error()); writeErr != nil {
						return writeErr
					}
					return err
				}
			}
		}
	}
}

func (w *terminalOutputWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return 0, w.err
	}
	written := 0
	for len(p) > 0 {
		if err := w.ctx.Err(); err != nil {
			return written, err
		}
		size := min(len(p), terminalOutputChunkBytes)
		chunk := append([]byte(nil), p[:size]...)
		select {
		case w.chunks <- chunk:
			written += size
			p = p[size:]
		default:
			w.err = errTerminalOutputBacklog
			w.failed <- w.err
			return written, w.err
		}
	}
	return written, nil
}

func (h *Host) findTerminalLink(id protocol.AgentID) *nativeAttachment {
	h.nativeMu.Lock()
	defer h.nativeMu.Unlock()
	link := h.native[id]
	if link == nil || !link.terminal {
		return nil
	}
	return link
}

func (h *Host) terminalLink(id protocol.AgentID) *nativeAttachment {
	link := h.findTerminalLink(id)
	if link == nil {
		return nil
	}
	link.mu.Lock()
	active := !link.closed && link.attached
	link.mu.Unlock()
	if !active {
		return nil
	}
	return link
}

func (h *Host) writeTerminalInput(id protocol.AgentID, data []byte) error {
	if h.terminalLink(id) == nil {
		return fmt.Errorf("%s embedded terminal is not open", id)
	}
	return h.s.(nativeSession).Write(id, data)
}

func (h *Host) resizeTerminal(id protocol.AgentID, cols, rows int) error {
	if h.terminalLink(id) == nil {
		return fmt.Errorf("%s embedded terminal is not open", id)
	}
	return h.s.(nativeSession).ResizeAgent(id, cols, rows)
}

func (h *Host) failTerminal(ctx context.Context, id protocol.AgentID, err error, sink *frameSink) {
	if h.terminalLink(id) == nil {
		_ = sink.failure(err)
		return
	}
	_ = sink.terminalError(id, err.Error())
	h.closeTerminal(id)
	_ = h.sendView(ctx, sink)
}

func (h *Host) closeTerminal(id protocol.AgentID) {
	link := h.findTerminalLink(id)
	if link == nil {
		return
	}
	h.closeNativeTransport(link)
	<-link.done
}

func (h *Host) closeNative(id protocol.AgentID) {
	h.nativeMu.Lock()
	link := h.native[id]
	h.nativeMu.Unlock()
	if link == nil {
		return
	}
	h.closeNativeTransport(link)
	<-link.done
}

func (h *Host) closeNativeTransport(link *nativeAttachment) {
	link.closeOnce.Do(func() {
		link.mu.Lock()
		link.closed = true
		conn, stream := link.conn, link.stream
		link.mu.Unlock()
		if conn != nil && stream != nil {
			_ = stream.WriteFrame('C', nil)
		}
		link.cancel()
		if link.listener != nil {
			_ = link.listener.Close()
		}
		if conn != nil {
			_ = conn.Close()
		}
	})
}

func (h *Host) closeNatives() {
	h.nativeMu.Lock()
	ids := make([]protocol.AgentID, 0, len(h.native))
	for id := range h.native {
		ids = append(ids, id)
	}
	h.nativeMu.Unlock()
	for _, id := range ids {
		h.closeNative(id)
	}
}

func (h *Host) finishNative(id protocol.AgentID, link *nativeAttachment) {
	h.closeNativeTransport(link)
	link.mu.Lock()
	attached := link.attached
	link.mu.Unlock()
	if attached {
		h.s.(nativeSession).Detach(id)
	}
	h.nativeMu.Lock()
	if h.native[id] == link {
		delete(h.native, id)
	}
	h.nativeMu.Unlock()
	close(link.done)
}

type nativeFrameWriter struct {
	mu   sync.Mutex
	conn net.Conn
}

type nativeReplayWriter struct {
	stream *nativeFrameWriter
	ready  chan struct{}
	once   sync.Once
}

func (w *nativeReplayWriter) Write(p []byte) (int, error) {
	<-w.ready
	return w.stream.Write(p)
}

func (w *nativeReplayWriter) release() { w.once.Do(func() { close(w.ready) }) }

func (w *nativeFrameWriter) Write(p []byte) (int, error) {
	if err := w.WriteFrame('O', p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (w *nativeFrameWriter) WriteFrame(kind byte, payload []byte) error {
	if len(payload) > maxCommandBytes {
		return errors.New("native frame exceeds limit")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	defer w.conn.SetWriteDeadline(time.Time{})
	var header [5]byte
	header[0] = kind
	binary.BigEndian.PutUint32(header[1:], uint32(len(payload)))
	if err := writeNativeAll(w.conn, header[:]); err != nil {
		return err
	}
	return writeNativeAll(w.conn, payload)
}

func writeNativeAll(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		p = p[n:]
	}
	return nil
}

func readNativeFrame(r io.Reader) (byte, []byte, error) {
	var header [5]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(header[1:])
	if n > maxCommandBytes {
		return 0, nil, errors.New("native frame exceeds limit")
	}
	payload := make([]byte, int(n))
	_, err := io.ReadFull(r, payload)
	return header[0], payload, err
}

// frameSink serializes the writers. The command reader never writes, but the
// event stream and the heartbeat do, and a frame must reach the GUI whole.
type frameSink struct {
	mu   sync.Mutex
	w    io.Writer
	fail error
	// terminal is set by the closed frame. It is what makes "closed" the end of
	// the stream rather than a race with the heartbeat.
	terminal bool
}

func (s *frameSink) view(v View) error { return s.write(writeFrame{Type: typeView, View: &v}) }

func (s *frameSink) event(e Event) error { return s.write(writeFrame{Type: typeEvent, Event: &e}) }

func (s *frameSink) terminalOutput(id protocol.AgentID, data []byte) error {
	return s.write(writeFrame{Type: typeTerminalOutput, Agent: wireAgent(id), Data: data})
}

func (s *frameSink) terminalError(id protocol.AgentID, text string) error {
	return s.write(writeFrame{Type: typeTerminalError, Agent: wireAgent(id), Text: text})
}

func (s *frameSink) failure(err error) error {
	return s.write(writeFrame{Type: typeError, Text: err.Error()})
}

func (s *frameSink) closed(text string) error {
	return s.writeFrame(writeFrame{Type: typeClosed, Text: text}, true)
}

// err reports the first write failure, which is what makes Serve return a real
// error instead of silently dropping frames.
func (s *frameSink) err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fail
}

// write encodes one frame and writes it as a single line.
//
// The frame is marshalled before the lock so two goroutines cannot interleave
// halves of a line, and the first failure is remembered rather than retried: a
// broken pipe stays broken, and every later write would just fail again.
func (s *frameSink) write(frame writeFrame) error {
	return s.writeFrame(frame, false)
}

// writeFrame writes one frame, and writes the stream's last frame when terminal
// is true. Setting the flag under the same lock that writes the line is what makes
// it the end of the stream: a heartbeat view already in flight when the session
// ends is dropped instead of arriving after the frame that said it was over.
func (s *frameSink) writeFrame(frame writeFrame, terminal bool) error {
	line, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal {
		return nil
	}
	if s.fail != nil {
		return s.fail
	}
	if _, err := s.w.Write(line); err != nil {
		s.fail = err
		return err
	}
	s.terminal = terminal
	return nil
}
