package guiipc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/models"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/session"
	"github.com/atfa/duo/internal/sessionstore"
)

func TestReadNativeFrameValidatesBoundedPayload(t *testing.T) {
	frame := make([]byte, 5+3)
	frame[0] = 'D'
	binary.BigEndian.PutUint32(frame[1:5], 3)
	copy(frame[5:], []byte("abc"))
	kind, payload, err := readNativeFrame(bytes.NewReader(frame))
	if err != nil || kind != 'D' || string(payload) != "abc" {
		t.Fatalf("readNativeFrame = %q %q %v", kind, payload, err)
	}
	oversized := make([]byte, 5)
	binary.BigEndian.PutUint32(oversized[1:], maxCommandBytes+1)
	if _, _, err := readNativeFrame(bytes.NewReader(oversized)); err == nil {
		t.Fatal("accepted oversized native frame")
	}
}

func TestSetDriverCatalogSurvivesProbeDeadline(t *testing.T) {
	root := guiTestRepo(t)
	binDir := t.TempDir()
	fakeAgentPath := filepath.Join(binDir, "example-agent")
	if err := os.WriteFile(fakeAgentPath, []byte("#!/bin/sh\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	pluginPath := filepath.Join(binDir, "duo-plugin-example")
	moduleRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", pluginPath, "./cmd/duo-plugin-example")
	build.Dir = moduleRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build example plugin: %v\n%s", err, out)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	const oldDriver = "gui-lifetime-fixture"
	service := session.New(session.Options{
		LaunchDir:          root,
		BaseDir:            t.TempDir(),
		Session:            "gui-driver-lifetime",
		Listen:             "127.0.0.1:0",
		Mode:               project.ModeFast,
		DefaultAgentDriver: oldDriver,
		AgentCommands: map[protocol.AgentID]string{
			protocol.Austin: fakeAgentPath,
			protocol.Tony:   fakeAgentPath,
		},
		RegisterDrivers: func() {
			driver.Register(oldDriver, func() driver.Handler { return guiLifetimeDriver{} })
		},
	})
	if _, err := service.Start(context.Background()); err != nil {
		t.Fatalf("start session: %v", err)
	}
	defer service.Close()

	ctx := context.Background()
	if err := service.Do(ctx, session.SetDriver{Agent: protocol.Austin, Driver: "example"}); err != nil {
		t.Fatalf("switch Austin to example: %v", err)
	}

	host := NewHost(service)
	host.loadCatalog(ctx, protocol.Austin)
	view := host.projectView(ctx, service.View(), service.StatusText(ctx))
	austin := view.Agents["austin"]
	if austin.Driver != "example" {
		t.Fatalf("Austin driver = %q, want example", austin.Driver)
	}
	if !austin.ModelsLoaded || len(austin.Models) == 0 || austin.ModelsError != "" {
		t.Fatalf("catalog after switching driver = loaded:%v models:%d error:%q", austin.ModelsLoaded, len(austin.Models), austin.ModelsError)
	}
}

func guiTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "gui@example.test"}, {"config", "user.name", "GUI Test"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "README.md"}, {"commit", "-q", "-m", "fixture"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	return dir
}

type guiLifetimeDriver struct{}

func (guiLifetimeDriver) Describe() (*driver.Manifest, error) {
	return &driver.Manifest{
		Protocol:       driver.ProtocolVersion,
		Name:           "gui-lifetime-fixture",
		Version:        "test",
		Agent:          driver.AgentInfo{CLI: "example-agent", DefaultCommand: "example-agent"},
		ModelReference: driver.ModelQualified,
		Capabilities: driver.Capabilities{
			Resume: driver.ResumeClient,
			Models: true,
			Bridge: driver.BridgeNone,
		},
	}, nil
}
func (guiLifetimeDriver) Probe() (*driver.ProbeResult, error) {
	return &driver.ProbeResult{Available: true, AgentPath: "/bin/true"}, nil
}
func (guiLifetimeDriver) Prepare(req driver.LaunchRequest) (*driver.LaunchPlan, error) {
	return &driver.LaunchPlan{Command: req.BaseCommand}, nil
}
func (guiLifetimeDriver) Models() (*driver.ModelList, error) {
	return &driver.ModelList{Models: []driver.Model{{Provider: "fixture", ID: "old"}}}, nil
}

type recordedAgentResize struct {
	agent      protocol.AgentID
	cols, rows int
}

type fakeNativeSession struct {
	*fakeSession
	muNative     sync.Mutex
	writer       io.Writer
	attached     bool
	attachN      int
	detachN      int
	input        [][]byte
	resizes      [][2]int
	agentResizes []recordedAgentResize
	sizes        map[protocol.AgentID][2]int
	attachedCh   chan struct{}
	detachedCh   chan struct{}
	inputCh      chan struct{}
	resizeCh     chan struct{}
	running      bool
	replay       []byte
}

func newFakeNativeSession() *fakeNativeSession {
	return &fakeNativeSession{
		fakeSession: newFakeSession(),
		running:     true,
		sizes: map[protocol.AgentID][2]int{
			protocol.Austin: {80, 24},
			protocol.Tony:   {80, 24},
		},
		attachedCh: make(chan struct{}, 4),
		detachedCh: make(chan struct{}, 4),
		inputCh:    make(chan struct{}, 8),
		resizeCh:   make(chan struct{}, 8),
	}
}

func (f *fakeNativeSession) Attach(_ protocol.AgentID, w io.Writer) ([]byte, error) {
	f.muNative.Lock()
	f.writer, f.attached = w, true
	f.attachN++
	f.muNative.Unlock()
	f.attachedCh <- struct{}{}
	if f.replay != nil {
		return append([]byte(nil), f.replay...), nil
	}
	return []byte("replay"), nil
}

func (f *fakeNativeSession) NativeRunning(protocol.AgentID) bool {
	f.muNative.Lock()
	defer f.muNative.Unlock()
	return f.running
}

func (f *fakeNativeSession) Detach(_ protocol.AgentID) {
	f.muNative.Lock()
	f.attached = false
	f.detachN++
	f.muNative.Unlock()
	f.detachedCh <- struct{}{}
}

func (f *fakeNativeSession) Write(_ protocol.AgentID, data []byte) error {
	f.muNative.Lock()
	f.input = append(f.input, append([]byte(nil), data...))
	f.muNative.Unlock()
	select {
	case f.inputCh <- struct{}{}:
	default:
	}
	return nil
}

func (f *fakeNativeSession) Resize(cols, rows int) error {
	f.muNative.Lock()
	f.resizes = append(f.resizes, [2]int{cols, rows})
	for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		f.sizes[agent] = [2]int{cols, rows}
	}
	f.muNative.Unlock()
	select {
	case f.resizeCh <- struct{}{}:
	default:
	}
	return nil
}

func (f *fakeNativeSession) ResizeAgent(agent protocol.AgentID, cols, rows int) error {
	f.muNative.Lock()
	f.agentResizes = append(f.agentResizes, recordedAgentResize{agent: agent, cols: cols, rows: rows})
	f.sizes[agent] = [2]int{cols, rows}
	f.muNative.Unlock()
	select {
	case f.resizeCh <- struct{}{}:
	default:
	}
	return nil
}

func (f *fakeNativeSession) nativeCounts() (attached, detached int) {
	f.muNative.Lock()
	defer f.muNative.Unlock()
	return f.attachN, f.detachN
}

func sendNativeFrame(t *testing.T, conn net.Conn, kind byte, payload []byte) {
	t.Helper()
	var header [5]byte
	header[0] = kind
	binary.BigEndian.PutUint32(header[1:], uint32(len(payload)))
	if err := writeNativeAll(conn, header[:]); err != nil {
		t.Fatal(err)
	}
	if err := writeNativeAll(conn, payload); err != nil {
		t.Fatal(err)
	}
}

func waitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestNativeAttachReplayInputResizeCloseAndDetach(t *testing.T) {
	fake := newFakeNativeSession()
	var mu sync.Mutex
	var client net.Conn
	launches := 0
	h := startHostSession(t, fake.fakeSession, fake, quiet, func(host *Host) {
		host.SetNativeLauncher(func(ctx context.Context, socket, _ string) error {
			mu.Lock()
			launches++
			mu.Unlock()
			conn, err := net.Dial("unix", socket)
			if err == nil {
				mu.Lock()
				client = conn
				mu.Unlock()
			}
			return err
		})
	})
	h.waitFrames(1)
	h.send(`{"type":"command","kind":"open_native","agent":"austin"}`)
	frames := h.waitFrames(2)
	if frames[1].View == nil || !frames[1].View.Agents["austin"].NativeOpen {
		t.Fatalf("pending attach was not projected as native_open: %+v", frames[1])
	}
	waitSignal(t, fake.attachedCh, "native attach")
	mu.Lock()
	conn := client
	mu.Unlock()
	if conn == nil {
		t.Fatal("native launcher did not return a client connection")
	}
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	kind, payload, err := readNativeFrame(conn)
	if err != nil || kind != 'O' || string(payload) != "replay" {
		t.Fatalf("initial replay = %q %q %v", kind, payload, err)
	}

	// A second click while one terminal is attached reports an error and does not
	// launch a second native client.
	h.send(`{"type":"command","kind":"open_native","agent":"austin"}`)
	frames = h.waitFrames(3)
	foundError := false
	for _, frame := range frames {
		foundError = foundError || frame.Type == typeError
	}
	if !foundError {
		t.Fatalf("duplicate native open did not report an error: %+v", frames)
	}
	mu.Lock()
	launchCount := launches
	mu.Unlock()
	if launchCount != 1 {
		t.Fatalf("native launcher called %d times for duplicate opens", launchCount)
	}

	sendNativeFrame(t, conn, 'D', []byte("input\r"))
	var resize [8]byte
	binary.BigEndian.PutUint32(resize[:4], 120)
	binary.BigEndian.PutUint32(resize[4:], 40)
	sendNativeFrame(t, conn, 'R', resize[:])
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		fake.muNative.Lock()
		ready := len(fake.input) == 1 && len(fake.resizes) == 1
		input := ""
		if ready {
			input = string(fake.input[0])
		}
		fake.muNative.Unlock()
		if ready {
			if input != "input\r" {
				t.Fatalf("native input = %q", input)
			}
			break
		}
		time.Sleep(time.Millisecond)
	}
	fake.muNative.Lock()
	if len(fake.resizes) != 1 || fake.resizes[0] != [2]int{120, 40} {
		t.Fatalf("native resize = %v", fake.resizes)
	}
	w := fake.writer
	fake.muNative.Unlock()
	if _, err := w.Write([]byte("live output")); err != nil {
		t.Fatalf("forward agent output: %v", err)
	}
	kind, payload, err = readNativeFrame(conn)
	if err != nil || kind != 'O' || string(payload) != "live output" {
		t.Fatalf("live output = %q %q %v", kind, payload, err)
	}

	// Closing from the GUI closes the client transport and detaches from the agent.
	h.send(`{"type":"command","kind":"close_native","agent":"austin"}`)
	waitSignal(t, fake.detachedCh, "GUI detach")
	if kind, payload, err := readNativeFrame(conn); err != nil || kind != 'C' || len(payload) != 0 {
		t.Fatalf("GUI close frame = %q %q %v; want clean close", kind, payload, err)
	}
	if _, _, err := readNativeFrame(conn); err == nil {
		t.Fatal("GUI close left the native socket open after the close frame")
	}
	_ = conn.Close()
	if attached, detached := fake.nativeCounts(); attached != 1 || detached != 1 {
		t.Fatalf("native counts after GUI close = %d/%d", attached, detached)
	}

	// The native client's quit frame has the same detach semantics.
	h.send(`{"type":"command","kind":"open_native","agent":"austin"}`)
	waitSignal(t, fake.attachedCh, "second native attach")
	mu.Lock()
	conn = client
	mu.Unlock()
	sendNativeFrame(t, conn, 'Q', nil)
	waitSignal(t, fake.detachedCh, "client detach")
	_ = conn.Close()
	h.send(`{"type":"command","kind":"quit"}`)
	if err := h.wait(); err != nil {
		t.Fatalf("Serve: %v", err)
	}
}

func TestEmbeddedTerminalRelayReplaysChunksForwardsInputAndResizes(t *testing.T) {
	fake := newFakeNativeSession()
	replay := bytes.Repeat([]byte("r"), terminalOutputChunkBytes*2+3)
	fake.replay = replay
	launcherCalls := 0
	h := startHostSession(t, fake.fakeSession, fake, quiet, func(host *Host) {
		host.SetNativeLauncher(func(context.Context, string, string) error {
			launcherCalls++
			return nil
		})
	})
	h.waitFrames(1)
	h.send(`{"type":"command","kind":"terminal_open","agent":"austin"}`)
	waitSignal(t, fake.attachedCh, "embedded terminal attach")
	h.waitFrames(5) // initial view, open view, then three bounded replay frames

	frames := h.rec.snapshot()
	var gotReplay []byte
	var outputFrames int
	for _, frame := range frames {
		if frame.Type != typeTerminalOutput {
			continue
		}
		outputFrames++
		if frame.Agent != "austin" || len(frame.Data) > terminalOutputChunkBytes {
			t.Fatalf("invalid terminal output frame: agent=%q bytes=%d", frame.Agent, len(frame.Data))
		}
		gotReplay = append(gotReplay, frame.Data...)
	}
	if outputFrames != 3 || !bytes.Equal(gotReplay, replay) {
		t.Fatalf("replay was sent in %d frames (%d bytes); want 3 frames and %d bytes", outputFrames, len(gotReplay), len(replay))
	}

	// A second embedded open is refused without replacing the live writer or
	// falling back to the legacy external launcher.
	h.send(`{"type":"command","kind":"terminal_open","agent":"austin"}`)
	frames = h.waitFrames(7)
	foundOpenError := false
	for _, frame := range frames {
		foundOpenError = foundOpenError || frame.Type == typeError
	}
	if !foundOpenError {
		t.Fatalf("duplicate terminal open was not rejected: %+v", frames)
	}
	if attached, detached := fake.nativeCounts(); attached != 1 || detached != 0 {
		t.Fatalf("duplicate terminal open changed attach/detach count to %d/%d", attached, detached)
	}

	input := []byte("echo embedded\r")
	encoded := base64.StdEncoding.EncodeToString(input)
	h.send(`{"type":"command","kind":"terminal_input","agent":"austin","data":"` + encoded + `"}`)
	h.send(`{"type":"command","kind":"terminal_resize","agent":"austin","cols":120,"rows":40}`)
	waitSignal(t, fake.inputCh, "terminal input forwarding")
	waitSignal(t, fake.resizeCh, "terminal resize forwarding")
	fake.muNative.Lock()
	if len(fake.input) != 1 || !bytes.Equal(fake.input[0], input) {
		t.Fatalf("terminal input = %q; want %q", fake.input, input)
	}
	if len(fake.agentResizes) != 1 || fake.agentResizes[0] != (recordedAgentResize{agent: protocol.Austin, cols: 120, rows: 40}) {
		t.Fatalf("agent-scoped terminal resize = %v; want Austin 120x40", fake.agentResizes)
	}
	if len(fake.resizes) != 0 || fake.sizes[protocol.Austin] != [2]int{120, 40} || fake.sizes[protocol.Tony] != [2]int{80, 24} {
		t.Fatalf("terminal resize affected unexpected PTYs: all=%v sizes=%v", fake.resizes, fake.sizes)
	}
	writer := fake.writer
	fake.muNative.Unlock()

	live := bytes.Repeat([]byte("o"), terminalOutputChunkBytes*2+7)
	if _, err := writer.Write(live); err != nil {
		t.Fatalf("write live terminal output: %v", err)
	}
	h.waitFrames(10) // duplicate-open frames, replay, then two chunks and a tail
	frames = h.rec.snapshot()
	var gotLive []byte
	for _, frame := range frames {
		if frame.Type == typeTerminalOutput {
			gotLive = append(gotLive, frame.Data...)
		}
	}
	if !bytes.Equal(gotLive, append(append([]byte(nil), replay...), live...)) {
		t.Fatalf("terminal output was not losslessly chunked: got %d bytes", len(gotLive))
	}

	h.send(`{"type":"command","kind":"terminal_close","agent":"austin"}`)
	waitSignal(t, fake.detachedCh, "embedded terminal close")
	frames = h.waitFrames(11)
	if frames[len(frames)-1].View == nil || frames[len(frames)-1].View.Agents["austin"].NativeOpen {
		t.Fatalf("terminal close did not clear native_open: %+v", frames[len(frames)-1])
	}
	if attached, detached := fake.nativeCounts(); attached != 1 || detached != 1 {
		t.Fatalf("embedded terminal attach/detach count = %d/%d", attached, detached)
	}
	fake.muNative.Lock()
	stillRunning := fake.running
	fake.muNative.Unlock()
	if !stillRunning {
		t.Fatal("closing the terminal stopped the agent")
	}
	if launcherCalls != 0 {
		t.Fatalf("embedded attach called the legacy launcher %d times", launcherCalls)
	}
	h.send(`{"type":"command","kind":"quit"}`)
	if err := h.wait(); err != nil {
		t.Fatalf("Serve: %v", err)
	}
}

func TestEmbeddedTerminalRejectsOversizedReplay(t *testing.T) {
	fake := newFakeNativeSession()
	fake.replay = make([]byte, terminalReplayMaxBytes+1)
	h := startHostSession(t, fake.fakeSession, fake, quiet)
	h.waitFrames(1)
	h.send(`{"type":"command","kind":"terminal_open","agent":"austin"}`)
	waitSignal(t, fake.attachedCh, "oversized replay attach")
	waitSignal(t, fake.detachedCh, "oversized replay detach")
	frames := h.waitFrames(4)
	foundError := false
	for _, frame := range frames {
		if frame.Type == typeTerminalOutput {
			t.Fatal("oversized replay was sent to the renderer")
		}
		if frame.Type == typeError && strings.Contains(frame.Text, "terminal replay exceeds") {
			foundError = true
		}
	}
	if !foundError {
		t.Fatalf("oversized replay did not produce an error: %+v", frames)
	}
	h.send(`{"type":"command","kind":"quit"}`)
	if err := h.wait(); err != nil {
		t.Fatalf("Serve: %v", err)
	}
}

func TestHostShutdownDetachesEmbeddedTerminal(t *testing.T) {
	fake := newFakeNativeSession()
	h := startHostSession(t, fake.fakeSession, fake, quiet)
	h.waitFrames(1)
	h.send(`{"type":"command","kind":"terminal_open","agent":"austin"}`)
	waitSignal(t, fake.attachedCh, "embedded terminal attach before shutdown")

	h.cancel()
	waitSignal(t, fake.detachedCh, "embedded terminal detach on host shutdown")
	if err := h.wait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Serve after shutdown = %v; want context.Canceled", err)
	}
	if attached, detached := fake.nativeCounts(); attached != 1 || detached != 1 {
		t.Fatalf("shutdown attach/detach count = %d/%d", attached, detached)
	}
}

func TestTerminalOutputQueueFailsClosedWithoutDroppingQueuedFrames(t *testing.T) {
	ctx, cancel := context.WithCancel(context.TODO())
	defer cancel()
	output := &terminalOutputWriter{
		ctx:    ctx,
		chunks: make(chan []byte, terminalOutputQueueDepth),
		failed: make(chan error, 1),
	}
	payload := bytes.Repeat([]byte("x"), terminalOutputChunkBytes*(terminalOutputQueueDepth+1))
	written, err := output.Write(payload)
	if !errors.Is(err, errTerminalOutputBacklog) {
		t.Fatalf("overflow write error = %v; want terminal backlog failure", err)
	}
	if want := terminalOutputChunkBytes * terminalOutputQueueDepth; written != want {
		t.Fatalf("overflow write accepted %d bytes; want exactly the bounded %d", written, want)
	}
	if len(output.chunks) != terminalOutputQueueDepth {
		t.Fatalf("queued chunks = %d; want bounded depth %d", len(output.chunks), terminalOutputQueueDepth)
	}

	var wire bytes.Buffer
	sink := &frameSink{w: &wire}
	host := NewHost(newFakeSession())
	if err := host.relayTerminalOutput(ctx, protocol.Austin, output, sink); !errors.Is(err, errTerminalOutputBacklog) {
		t.Fatalf("relay error = %v; want backlog failure", err)
	}
	decoder := json.NewDecoder(&wire)
	var received []byte
	for i := 0; i < terminalOutputQueueDepth; i++ {
		var frame writeFrame
		if err := decoder.Decode(&frame); err != nil {
			t.Fatalf("decode queued terminal frame %d: %v", i, err)
		}
		if frame.Type != typeTerminalOutput || len(frame.Data) != terminalOutputChunkBytes {
			t.Fatalf("queued frame %d = type %q, bytes %d", i, frame.Type, len(frame.Data))
		}
		received = append(received, frame.Data...)
	}
	var failure writeFrame
	if err := decoder.Decode(&failure); err != nil {
		t.Fatalf("decode terminal error frame: %v", err)
	}
	if failure.Type != typeTerminalError || failure.Agent != "austin" || !strings.Contains(failure.Text, "backlog") {
		t.Fatalf("overflow failure frame = %+v", failure)
	}
	if !bytes.Equal(received, payload[:len(received)]) {
		t.Fatal("queued terminal output was dropped or reordered before the failure")
	}
}

func TestNativeAttachRejectsExitedAgentBeforeLaunchingTerminal(t *testing.T) {
	fake := newFakeNativeSession()
	fake.running = false
	launches := 0
	h := startHostSession(t, fake.fakeSession, fake, quiet, func(host *Host) {
		host.SetNativeLauncher(func(context.Context, string, string) error {
			launches++
			return nil
		})
	})
	h.waitFrames(1)
	h.send(`{"type":"command","kind":"open_native","agent":"austin"}`)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, frame := range h.rec.snapshot() {
			if frame.Type == typeError {
				if !strings.Contains(frame.Text, "agent process has exited") {
					t.Fatalf("error frame = %q", frame.Text)
				}
				if launches != 0 {
					t.Fatalf("launched terminal %d times for exited process", launches)
				}
				h.send(`{"type":"command","kind":"quit"}`)
				if err := h.wait(); err != nil {
					t.Fatal(err)
				}
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("exited agent did not produce a GUI error")
}

func TestNativeAttachAllowsSlowLargeInitialReplay(t *testing.T) {
	fake := newFakeNativeSession()
	fake.replay = bytes.Repeat([]byte("x"), 512<<10)
	clientCh := make(chan net.Conn, 1)
	h := startHostSession(t, fake.fakeSession, fake, quiet, func(host *Host) {
		host.SetNativeLauncher(func(_ context.Context, socket, _ string) error {
			conn, err := net.Dial("unix", socket)
			if err == nil {
				clientCh <- conn
			}
			return err
		})
	})
	h.waitFrames(1)
	h.send(`{"type":"command","kind":"open_native","agent":"austin"}`)
	conn := <-clientCh
	defer conn.Close()
	// Simulate Terminal taking time to create its window and begin consuming output.
	time.Sleep(1200 * time.Millisecond)
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	kind, payload, err := readNativeFrame(conn)
	if err != nil || kind != 'O' || len(payload) != len(fake.replay) {
		t.Fatalf("slow initial replay = kind %q, payload %d bytes, err %v", kind, len(payload), err)
	}
	sendNativeFrame(t, conn, 'Q', nil)
	h.send(`{"type":"command","kind":"quit"}`)
	if err := h.wait(); err != nil {
		t.Fatal(err)
	}
}

func TestNativeParentCancellationSendsCleanCloseFrame(t *testing.T) {
	fake := newFakeNativeSession()
	clientCh := make(chan net.Conn, 1)
	h := startHostSession(t, fake.fakeSession, fake, quiet, func(host *Host) {
		host.SetNativeLauncher(func(_ context.Context, socket, _ string) error {
			conn, err := net.Dial("unix", socket)
			if err == nil {
				clientCh <- conn
			}
			return err
		})
	})
	h.waitFrames(1)
	h.send(`{"type":"command","kind":"open_native","agent":"austin"}`)
	conn := <-clientCh
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if kind, _, err := readNativeFrame(conn); err != nil || kind != 'O' {
		t.Fatalf("initial replay = %q, %v", kind, err)
	}
	h.cancel()
	if kind, payload, err := readNativeFrame(conn); err != nil || kind != 'C' || len(payload) != 0 {
		t.Fatalf("parent cancellation frame = %q %q %v; want clean close", kind, payload, err)
	}
	if err := h.wait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Serve after parent cancellation = %v", err)
	}
}

func TestNativePendingLaunchIsCanceledByGUIClose(t *testing.T) {
	fake := newFakeNativeSession()
	launchStarted := make(chan struct{})
	h := startHostSession(t, fake.fakeSession, fake, quiet, func(host *Host) {
		host.SetNativeLauncher(func(ctx context.Context, _, _ string) error {
			close(launchStarted)
			<-ctx.Done()
			return ctx.Err()
		})
	})
	h.waitFrames(1)
	h.send(`{"type":"command","kind":"open_native","agent":"austin"}`)
	waitSignal(t, launchStarted, "pending native launcher")
	frames := h.waitFrames(2)
	if frames[1].View == nil || !frames[1].View.Agents["austin"].NativeOpen {
		t.Fatalf("pending attach was not marked open: %+v", frames[1])
	}
	h.send(`{"type":"command","kind":"close_native","agent":"austin"}`)
	frames = h.waitFrames(3)
	if frames[len(frames)-1].View == nil || frames[len(frames)-1].View.Agents["austin"].NativeOpen {
		t.Fatalf("close did not clear pending native state: %+v", frames[len(frames)-1])
	}
	h.send(`{"type":"command","kind":"quit"}`)
	if err := h.wait(); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if attached, detached := fake.nativeCounts(); attached != 0 || detached != 0 {
		t.Fatalf("pending native launch unexpectedly attached: %d/%d", attached, detached)
	}
}

// fakeSession is the seam with nothing live behind it: a view, a real event bus,
// and the commands the host dispatched. It exists because the host depends on
// the Session interface rather than *session.Service, which is the same property
// that makes a second frontend possible.
type fakeSession struct {
	mu      sync.Mutex
	view    session.View
	status  string
	history []sessionstore.TUIEntry
	bus     *events.Bus

	cancels     []func()
	got         []session.Command
	doErr       error
	modelResult []models.Model
	modelErr    error
}

func newFakeSession() *fakeSession {
	return &fakeSession{view: testView(), status: "status line", bus: events.NewBus()}
}

func (f *fakeSession) View() session.View {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.view
}

func (f *fakeSession) Subscribe(buffer int) (session.Subscription, func()) {
	ch, cancel := f.bus.Subscribe(buffer)
	f.mu.Lock()
	f.cancels = append(f.cancels, cancel)
	f.mu.Unlock()
	return session.Subscription{View: f.View(), Events: ch}, cancel
}

func (f *fakeSession) Do(_ context.Context, cmd session.Command) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, cmd)
	return f.doErr
}

func (f *fakeSession) StatusText(context.Context) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}

func (f *fakeSession) History() []sessionstore.TUIEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sessionstore.TUIEntry(nil), f.history...)
}

func (f *fakeSession) Models(context.Context, protocol.AgentID) ([]models.Model, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]models.Model(nil), f.modelResult...), f.modelErr
}

func (f *fakeSession) emit(e events.Event) { f.bus.Emit(e) }

// closeEvents closes the stream the host subscribed to, which is what a session
// closing its bus looks like from the host's side.
func (f *fakeSession) closeEvents() {
	f.mu.Lock()
	cancels := append([]func(){}, f.cancels...)
	f.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (f *fakeSession) commands() []session.Command {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]session.Command(nil), f.got...)
}

func (f *fakeSession) failDo(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.doErr = err
}

// frameRecorder decodes the host's display stream in the background so a test can
// assert on frames without racing the writer.
type frameRecorder struct {
	mu     sync.Mutex
	frames []writeFrame
	dec    *json.Decoder
}

func newFrameRecorder(r io.Reader) *frameRecorder {
	rec := &frameRecorder{dec: json.NewDecoder(r)}
	go rec.collect()
	return rec
}

func (r *frameRecorder) collect() {
	for {
		var f writeFrame
		if err := r.dec.Decode(&f); err != nil {
			return
		}
		r.mu.Lock()
		r.frames = append(r.frames, f)
		r.mu.Unlock()
	}
}

func (r *frameRecorder) snapshot() []writeFrame {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]writeFrame(nil), r.frames...)
}

// hostHarness runs one host over pipes, the way the CLI runs it over a child
// process, and cleans up every end of every pipe.
type hostHarness struct {
	t      *testing.T
	fake   *fakeSession
	host   *Host
	inW    *io.PipeWriter
	outW   *io.PipeWriter
	rec    *frameRecorder
	done   chan error
	cancel context.CancelFunc
}

func startHost(t *testing.T, fake *fakeSession, tune ...func(*Host)) *hostHarness {
	return startHostSession(t, fake, fake, tune...)
}

func startHostSession(t *testing.T, fake *fakeSession, s Session, tune ...func(*Host)) *hostHarness {
	t.Helper()

	cmdR, cmdW := io.Pipe()
	outR, outW := io.Pipe()
	host := NewHost(s)
	for _, fn := range tune {
		fn(host)
	}

	ctx, cancel := context.WithCancel(context.Background())
	h := &hostHarness{
		t:      t,
		fake:   fake,
		host:   host,
		inW:    cmdW,
		outW:   outW,
		rec:    newFrameRecorder(outR),
		done:   make(chan error, 1),
		cancel: cancel,
	}
	go func() { h.done <- host.Serve(ctx, cmdR, outW) }()
	t.Cleanup(func() {
		cancel()
		_ = cmdW.Close()
		_ = outW.Close()
	})
	return h
}

// quiet pins the heartbeat far out of the way for tests that assert exact frame
// sequences, so a ticker cannot insert a frame between two assertions.
func quiet(h *Host) { h.heartbeat = time.Hour }

func (h *hostHarness) send(line string) {
	h.t.Helper()
	if _, err := io.WriteString(h.inW, line+"\n"); err != nil {
		h.t.Fatalf("send command: %v", err)
	}
}

func (h *hostHarness) wait() error {
	h.t.Helper()
	select {
	case err := <-h.done:
		return err
	case <-time.After(5 * time.Second):
		h.t.Fatal("Serve did not return")
		return nil
	}
}

func (h *hostHarness) waitFrames(n int) []writeFrame {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		frames := h.rec.snapshot()
		if len(frames) >= n {
			return frames
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %d frames; got %d: %+v", n, len(frames), frames)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (h *hostHarness) waitCommands(n int) []session.Command {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		cmds := h.fake.commands()
		if len(cmds) >= n {
			return cmds
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %d commands; got %d: %+v", n, len(cmds), cmds)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestServeSendsViewThenHistoryThenEvents(t *testing.T) {
	fake := newFakeSession()
	fake.history = []sessionstore.TUIEntry{{
		Time:  eventTime,
		Pane:  "Austin",
		Text:  "earlier line",
		Label: "Austin → Tony",
	}}
	h := startHost(t, fake, quiet)

	frames := h.waitFrames(2)
	if frames[0].Type != typeView || frames[0].View == nil {
		t.Fatalf("first frame = %+v; want a view", frames[0])
	}
	if frames[0].View.Session != fake.view.SessionID || frames[0].View.Status != "status line" {
		t.Errorf("view session/status = %q/%q", frames[0].View.Session, frames[0].View.Status)
	}
	if len(frames[0].View.Notices) != 1 {
		t.Errorf("view notices = %v; driver diagnostics must reach the window", frames[0].View.Notices)
	}
	if frames[1].Type != typeEvent || frames[1].Event == nil {
		t.Fatalf("second frame = %+v; want a history event", frames[1])
	}
	if frames[1].Event.Text != "earlier line" || frames[1].Event.Agent != "austin" || frames[1].Event.Kind != "assistant" {
		t.Errorf("history event = %+v", frames[1].Event)
	}
	if frames[1].Event.Label != "Austin → Tony" {
		t.Errorf("history label = %q; replay must keep the direction header", frames[1].Event.Label)
	}

	fake.emit(events.Event{Time: eventTime, Kind: events.KindPeer, Agent: protocol.Tony, Peer: protocol.Austin, Text: "live"})
	frames = h.waitFrames(4)
	if frames[2].Type != typeEvent || frames[2].Event.Text != "live" || frames[2].Event.Kind != "peer" {
		t.Errorf("live event frame = %+v", frames[2])
	}
	if frames[3].Type != typeView {
		t.Errorf("frame after an event = %+v; want a refreshed view", frames[3])
	}

	h.send(`{"type":"command","kind":"quit"}`)
	if err := h.wait(); err != nil {
		t.Errorf("Serve returned %v after quit; want nil", err)
	}
}

func TestServeLoadsDriverCatalogAndPublishesItAsynchronously(t *testing.T) {
	fake := newFakeSession()
	fake.modelResult = []models.Model{{Provider: "provider", ID: "model-id", Thinking: true}}
	h := startHost(t, fake, func(host *Host) { host.heartbeat = 5 * time.Millisecond })
	frames := h.waitFrames(2)
	deadline := time.Now().Add(5 * time.Second)
	for {
		frames = h.rec.snapshot()
		found := false
		for _, frame := range frames {
			if frame.View != nil {
				a := frame.View.Agents["austin"]
				if a.ModelsLoaded && len(a.Models) == 1 {
					found = true
					if a.Models[0].Reference != "provider/model-id" || !a.Models[0].Thinking {
						t.Errorf("catalog option = %+v", a.Models[0])
					}
				}
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("catalog was not published; frames=%+v", frames)
		}
		time.Sleep(time.Millisecond)
	}
	if frames[0].View == nil || frames[0].Type != typeView {
		t.Fatalf("initial frame = %+v", frames[0])
	}
	h.send(`{"type":"command","kind":"quit"}`)
	if err := h.wait(); err != nil {
		t.Fatalf("Serve: %v", err)
	}
}

func TestServeDispatchesCommandsAndReportsFailures(t *testing.T) {
	fake := newFakeSession()
	h := startHost(t, fake, quiet)
	h.waitFrames(1)

	h.send(`{"type":"command","kind":"submit_task","text":"ship it"}`)
	h.send(`{"type":"command","kind":"restart_agent","agent":"tony"}`)
	got := h.waitCommands(2)
	if want := (session.SubmitTask{Text: "ship it"}); got[0] != want {
		t.Errorf("command 0 = %#v; want %#v", got[0], want)
	}
	if want := (session.RestartAgent{Agent: protocol.Tony}); got[1] != want {
		t.Errorf("command 1 = %#v; want %#v", got[1], want)
	}

	// A frame the protocol does not know is reported, not fatal: the window
	// stays open so the human can see why their keystroke did nothing.
	h.send(`{"type":"command","kind":"unknown_kind"}`)
	frames := h.waitFrames(2)
	if frames[1].Type != typeError || !strings.Contains(frames[1].Text, "unknown command kind") {
		t.Errorf("bad frame produced %+v", frames[1])
	}

	// A session that rejects a known verb is reported the same way.
	fake.failDo(errors.New("session is closed"))
	h.send(`{"type":"command","kind":"escalate_to_goal"}`)
	frames = h.waitFrames(3)
	if frames[2].Type != typeError || !strings.Contains(frames[2].Text, "session is closed") {
		t.Errorf("rejected command produced %+v", frames[2])
	}
	if cmds := h.waitCommands(3); len(cmds) != 3 {
		t.Errorf("dispatched %d commands; want 3", len(cmds))
	}
}

func TestServeReturnsWhenSessionClosesItsEvents(t *testing.T) {
	fake := newFakeSession()
	h := startHost(t, fake, quiet)
	h.waitFrames(1)

	fake.closeEvents()
	frames := h.waitFrames(2)
	if frames[1].Type != typeClosed {
		t.Fatalf("frame after the bus closed = %+v; want closed", frames[1])
	}
	if err := h.wait(); err != nil {
		t.Errorf("Serve returned %v after the session ended; want nil", err)
	}
}

func TestServeReturnsWhenTheCommandStreamEnds(t *testing.T) {
	fake := newFakeSession()
	h := startHost(t, fake, quiet)
	h.waitFrames(1)

	if err := h.inW.Close(); err != nil {
		t.Fatalf("close command stream: %v", err)
	}
	if err := h.wait(); err != nil {
		t.Errorf("Serve returned %v after the frontend exited; want nil", err)
	}
}

// TestServeReturnsWhenTheContextIsCancelled covers the one exit that is neither
// the frontend's nor the session's: the process running the host is going away.
func TestServeReturnsWhenTheContextIsCancelled(t *testing.T) {
	fake := newFakeSession()
	h := startHost(t, fake, quiet)
	h.waitFrames(1)

	h.cancel()
	if err := h.wait(); !errors.Is(err, context.Canceled) {
		t.Errorf("Serve returned %v after cancellation; want context.Canceled", err)
	}
}

// TestServeSendsNothingAfterTheSessionEnds pins that the closed frame really is
// the end of the stream. The heartbeat is still running when the bus closes, so
// without the terminal flag a view can land after it and contradict the frame the
// window just read.
func TestServeSendsNothingAfterTheSessionEnds(t *testing.T) {
	fake := newFakeSession()
	h := startHost(t, fake, func(host *Host) { host.heartbeat = time.Millisecond })
	h.waitFrames(1)

	fake.closeEvents()
	if err := h.wait(); err != nil {
		t.Errorf("Serve returned %v after the session ended; want nil", err)
	}
	// Let any heartbeat already in flight land before deciding.
	time.Sleep(20 * time.Millisecond)

	frames := h.rec.snapshot()
	closed := -1
	for i, frame := range frames {
		if frame.Type == typeClosed {
			closed = i
		}
	}
	if closed < 0 {
		t.Fatalf("no closed frame: %+v", frames)
	}
	if closed != len(frames)-1 {
		t.Errorf("frames followed the closed frame: %+v", frames[closed+1:])
	}
}

func TestServeHeartbeatsTheView(t *testing.T) {
	fake := newFakeSession()
	h := startHost(t, fake, func(host *Host) { host.heartbeat = 10 * time.Millisecond })

	frames := h.waitFrames(3)
	for i, f := range frames {
		if f.Type != typeView || f.View == nil {
			t.Fatalf("frame %d = %+v; heartbeat must send views only", i, f)
		}
	}
}

func TestServeReportsAWriteFailure(t *testing.T) {
	host := NewHost(newFakeSession())
	host.heartbeat = time.Hour

	err := host.Serve(context.Background(), strings.NewReader(""), failingWriter{})
	if !errors.Is(err, errWriteRefused) {
		t.Fatalf("Serve returned %v; want the write failure", err)
	}
}

var errWriteRefused = errors.New("write refused")

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errWriteRefused }
