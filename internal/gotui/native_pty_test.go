//go:build unix

package gotui

import (
	"context"
	"os"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/terminal"
	"github.com/creack/pty"
	"github.com/gdamore/tcell/v3"
	"golang.org/x/term"
)

// TestNativeAttachUsesPTYAndRestoresTerminal exercises the real handoff path:
// raw terminal state, attachment, byte forwarding, detach and restoration.
func TestNativeAttachUsesPTYAndRestoresTerminal(t *testing.T) {
	fake := newFakeSession()
	app := New(fake)
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	screen.SetSize(80, 24)
	defer screen.Fini()
	app.screen = screen
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	app.ctx = ctx

	master, slave, err := pty.Open()
	if err != nil {
		t.Skipf("open pty: %v", err)
	}
	defer master.Close()

	before, err := term.GetState(int(slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	dup, err := syscall.Dup(int(slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	dupFile := os.NewFile(uintptr(dup), "pty-state")
	defer dupFile.Close()

	tty := &terminal.TTY{File: slave}
	app.openTTY = func() (*terminal.TTY, error) { return tty, nil }
	go func() {
		time.Sleep(100 * time.Millisecond)
		_, _ = master.Write([]byte("hello\x1d"))
	}()
	if err := app.nativeAttach(protocol.Austin); err != nil {
		t.Fatal(err)
	}

	if got := string(fake.writes[protocol.Austin][0]); got != "hello" {
		t.Fatalf("forwarded native input = %q, want hello", got)
	}
	if len(fake.detached) != 1 || fake.detached[0] != protocol.Austin {
		t.Fatalf("detached = %v, want Austin", fake.detached)
	}
	if app.native != "" || app.nativeTTY != nil {
		t.Fatalf("native state not cleared: agent=%q tty=%v", app.native, app.nativeTTY)
	}

	after, err := term.GetState(int(dupFile.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("terminal state not restored:\nbefore %+v\nafter  %+v", before, after)
	}
}
