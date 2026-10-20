//go:build unix

package gotui

import (
	"bytes"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gdamore/tcell/v3"
)

type splitTty struct {
	mu   sync.Mutex
	in   chan []byte
	cur  []byte
	out  bytes.Buffer
	ws   tcell.WindowSize
	done chan struct{}
	once sync.Once
}

func newSplitTty() *splitTty {
	return &splitTty{in: make(chan []byte, 16), done: make(chan struct{}), ws: tcell.WindowSize{Width: 80, Height: 24}}
}

func (f *splitTty) Start() error { return nil }
func (f *splitTty) Stop() error  { return nil }
func (f *splitTty) Drain() error {
	f.once.Do(func() { close(f.done) })
	return nil
}
func (f *splitTty) Close() error                          { return f.Drain() }
func (f *splitTty) NotifyResize(chan<- bool)              {}
func (f *splitTty) WindowSize() (tcell.WindowSize, error) { return f.ws, nil }
func (f *splitTty) Write(p []byte) (int, error)           { return f.out.Write(p) }
func (f *splitTty) Read(p []byte) (int, error) {
	for len(f.cur) == 0 {
		select {
		case chunk := <-f.in:
			f.cur = chunk
		case <-f.done:
			return 0, errors.New("closed")
		}
	}
	n := copy(p, f.cur)
	f.cur = f.cur[n:]
	return n, nil
}

func TestTcellDecodesSplitUTF8(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("LC_ALL", "en_US.UTF-8")

	tty := newSplitTty()
	screen, err := tcell.NewTerminfoScreenFromTty(tty)
	if err != nil {
		t.Fatal(err)
	}
	tty.in <- []byte("\x1b[?62;1;6c")
	initDone := make(chan error, 1)
	go func() { initDone <- screen.Init() }()
	select {
	case err := <-initDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Init hung")
	}
	defer screen.Fini()

	for _, b := range []byte("中") {
		tty.in <- []byte{b}
		time.Sleep(20 * time.Millisecond)
	}
	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case ev := <-screen.EventQ():
			if key, ok := ev.(*tcell.EventKey); ok && key.Key() == tcell.KeyRune {
				if key.Str() != "中" {
					t.Fatalf("rune = %q, want 中", key.Str())
				}
				return
			}
		case <-deadline:
			t.Fatal("did not receive the complete rune")
		}
	}
}
