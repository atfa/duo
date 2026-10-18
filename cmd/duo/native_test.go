package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/internal/guiipc"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/session"
	"github.com/creack/pty"
)

func TestNativeTerminalSizeFallsBackWhenNewWindowHasNoSize(t *testing.T) {
	if cols, rows := nativeTerminalSize(0, 0); cols != 80 || rows != 24 {
		t.Fatalf("nativeTerminalSize(0, 0) = %dx%d; want 80x24", cols, rows)
	}
	if cols, rows := nativeTerminalSize(80, 24); cols != 80 || rows != 24 {
		t.Fatalf("nativeTerminalSize(80, 24) = %dx%d; want unchanged", cols, rows)
	}
}

func TestNativeAttachRunsOverPTY(t *testing.T) {
	path := filepath.Join(t.TempDir(), "attach.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate native CLI package source")
	}
	binaryPath := filepath.Join(t.TempDir(), "duo")
	build := exec.Command("go", "build", "-o", binaryPath, ".")
	build.Dir = filepath.Dir(source)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build native CLI: %v\n%s", err, output)
	}
	command := exec.Command(binaryPath, "native", "--socket", path, "--agent", "austin")
	command.Env = os.Environ()
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = terminal.Close()
		if command.Process != nil {
			_ = command.Process.Kill()
		}
	}()

	_ = listener.(*net.UnixListener).SetDeadline(time.Now().Add(3 * time.Second))
	conn, err := listener.Accept()
	if err != nil {
		t.Fatalf("native client did not connect: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	kind, payload, err := readPTYNativeFrame(conn)
	if err != nil || kind != 'R' || len(payload) != 8 {
		t.Fatalf("initial resize frame = %q %v bytes, %v", kind, len(payload), err)
	}
	cols, rows := binary.BigEndian.Uint32(payload[:4]), binary.BigEndian.Uint32(payload[4:])
	if cols != 80 || rows != 24 {
		t.Fatalf("initial terminal size = %dx%d, want 80x24", cols, rows)
	}
	if err := writePTYNativeFrame(conn, 'O', []byte("pty-ready")); err != nil {
		t.Fatal(err)
	}
	ptyOutput := make(chan []byte, 1)
	go func() {
		buffer := make([]byte, 4096)
		var output []byte
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			_ = terminal.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			n, readErr := terminal.Read(buffer)
			output = append(output, buffer[:n]...)
			if bytes.Contains(output, []byte("pty-ready")) {
				ptyOutput <- output
				return
			}
			if readErr != nil && !isTimeout(readErr) {
				break
			}
		}
		ptyOutput <- output
	}()
	select {
	case output := <-ptyOutput:
		if !bytes.Contains(output, []byte("pty-ready")) {
			t.Fatalf("PTY did not receive native output: %q", output)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for native output in PTY")
	}
	if _, err := terminal.Write([]byte("input\r")); err != nil {
		t.Fatal(err)
	}
	kind, payload, err = readPTYNativeFrame(conn)
	if err != nil || kind != 'D' || string(payload) != "input\r" {
		t.Fatalf("native input frame = %q %q %v", kind, payload, err)
	}
	if _, err := terminal.Write([]byte{0x1d}); err != nil {
		t.Fatal(err)
	}
	kind, _, err = readPTYNativeFrame(conn)
	if err != nil || kind != 'Q' {
		t.Fatalf("detach frame = %q %v", kind, err)
	}

	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	select {
	case err := <-wait:
		if err != nil {
			t.Fatalf("native PTY helper exited: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("native PTY helper did not exit after detach")
	}
}

type nativePTYFixtureDriver struct{ name string }

func (d nativePTYFixtureDriver) Describe() (*driver.Manifest, error) {
	return &driver.Manifest{
		Protocol:       driver.ProtocolVersion,
		Name:           d.name,
		Version:        "test",
		Agent:          driver.AgentInfo{CLI: "native-pty-fixture"},
		ModelReference: driver.ModelBare,
		Capabilities:   driver.Capabilities{Resume: driver.ResumeNone, Bridge: driver.BridgeNone, PTYFallback: true},
	}, nil
}
func (nativePTYFixtureDriver) Probe() (*driver.ProbeResult, error) {
	return &driver.ProbeResult{Available: true}, nil
}
func (nativePTYFixtureDriver) Prepare(req driver.LaunchRequest) (*driver.LaunchPlan, error) {
	return &driver.LaunchPlan{Command: req.BaseCommand}, nil
}

func TestNativeCLIHostServicePTYChain(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "native@example.test"}, {"config", "user.name", "Native Test"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "README.md"}, {"commit", "-q", "-m", "fixture"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	agentPath := filepath.Join(t.TempDir(), "fake-agent")
	if err := os.WriteFile(agentPath, []byte("#!/bin/sh\necho native-agent-ready\nwhile IFS= read -r line; do echo native-agent-received:$line; done\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	const driverName = "native-pty-chain-fixture"
	service := session.New(session.Options{
		LaunchDir: root, BaseDir: t.TempDir(), Session: "native-pty-chain", Listen: "127.0.0.1:0",
		Mode: project.ModeFast, DefaultAgentDriver: driverName,
		AgentCommands: map[protocol.AgentID]string{protocol.Austin: agentPath, protocol.Tony: agentPath},
		RegisterDrivers: func() {
			driver.Register(driverName, func() driver.Handler { return nativePTYFixtureDriver{name: driverName} })
		},
	})
	if _, err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { service.Close() })
	if !service.NativeRunning(protocol.Austin) {
		t.Fatal("fixture Austin process did not remain running")
	}

	cmdR, cmdW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	host := guiipc.NewHost(service)
	nativePTY := make(chan *os.File, 1)
	nativeCmd := make(chan *exec.Cmd, 1)
	host.SetNativeLauncher(func(_ context.Context, socket, agent string) error {
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		cmd := exec.Command(executable, "-test.run=^TestNativeCLIProcessChild$")
		cmd.Env = append(os.Environ(), "DUO_NATIVE_CHILD=1", "DUO_NATIVE_SOCKET="+socket, "DUO_NATIVE_AGENT="+agent)
		terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 80, Rows: 24})
		if err == nil {
			nativePTY <- terminal
			nativeCmd <- cmd
		}
		return err
	})
	hostDone := make(chan error, 1)
	go func() { hostDone <- host.Serve(ctx, cmdR, outW) }()
	defer func() { _ = cmdW.Close(); _ = outW.Close() }()

	attached := make(chan struct{})
	var attachedOnce sync.Once
	var outputMu sync.Mutex
	var terminalOutput bytes.Buffer
	go func() {
		scanner := bufio.NewScanner(outR)
		for scanner.Scan() {
			var frame struct {
				Type string `json:"type"`
				View struct {
					Agents map[string]struct {
						NativeOpen bool `json:"native_open"`
					} `json:"agents"`
				} `json:"view"`
			}
			if json.Unmarshal(scanner.Bytes(), &frame) == nil && frame.View.Agents["austin"].NativeOpen {
				attachedOnce.Do(func() { close(attached) })
			}
		}
	}()
	if _, err := fmt.Fprintln(cmdW, `{"type":"command","kind":"open_native","agent":"austin"}`); err != nil {
		t.Fatal(err)
	}
	var terminal *os.File
	var cli *exec.Cmd
	select {
	case terminal = <-nativePTY:
		cli = <-nativeCmd
	case <-time.After(5 * time.Second):
		t.Fatal("GUI host did not launch native CLI")
	}
	t.Cleanup(func() {
		_ = terminal.Close()
		if cli.Process != nil {
			_ = cli.Process.Kill()
		}
	})
	select {
	case <-attached:
	case <-time.After(5 * time.Second):
		t.Fatal("GUI host did not publish native attachment")
	}
	go func() {
		buffer := make([]byte, 4096)
		for {
			n, err := terminal.Read(buffer)
			if n > 0 {
				outputMu.Lock()
				_, _ = terminalOutput.Write(buffer[:n])
				outputMu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	waitTerminalText := func(needle string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			outputMu.Lock()
			found := bytes.Contains(terminalOutput.Bytes(), []byte(needle))
			outputMu.Unlock()
			if found {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		outputMu.Lock()
		defer outputMu.Unlock()
		t.Fatalf("native PTY did not show %q; output=%q", needle, terminalOutput.String())
	}
	waitTerminalText("native-agent-ready")
	if _, err := terminal.Write([]byte("hello\r")); err != nil {
		t.Fatal(err)
	}
	waitTerminalText("native-agent-received:hello")
	if _, err := terminal.Write([]byte{0x1d}); err != nil {
		t.Fatal(err)
	}
	wait := make(chan error, 1)
	go func() { wait <- cli.Wait() }()
	select {
	case err := <-wait:
		if err != nil {
			t.Fatalf("native CLI exited: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("native CLI did not detach")
	}
	if !service.NativeRunning(protocol.Austin) {
		t.Fatal("agent process stopped when the native terminal detached")
	}
	cancel()
	_ = cmdW.Close()
	select {
	case <-hostDone:
	case <-time.After(5 * time.Second):
		t.Fatal("GUI host did not stop")
	}
}

func TestNativeCLIProcessChild(t *testing.T) {
	if os.Getenv("DUO_NATIVE_CHILD") != "1" {
		return
	}
	err := runNativeAttach(context.Background(), []string{"--socket", os.Getenv("DUO_NATIVE_SOCKET"), "--agent", os.Getenv("DUO_NATIVE_AGENT")})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

func readPTYNativeFrame(r io.Reader) (byte, []byte, error) {
	var header [5]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, nil, err
	}
	length := binary.BigEndian.Uint32(header[1:])
	if length > 1<<20 {
		return 0, nil, io.ErrShortBuffer
	}
	payload := make([]byte, int(length))
	_, err := io.ReadFull(r, payload)
	return header[0], payload, err
}

func writePTYNativeFrame(w io.Writer, kind byte, payload []byte) error {
	var header [5]byte
	header[0] = kind
	binary.BigEndian.PutUint32(header[1:], uint32(len(payload)))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func isTimeout(err error) bool {
	type timeout interface{ Timeout() bool }
	var netErr timeout
	return errors.As(err, &netErr) && netErr.Timeout()
}
