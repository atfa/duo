package terminal

import (
	"golang.org/x/term"
	"os"
)

type TTY struct {
	File  *os.File
	saved *term.State
}

func Open() (*TTY, error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	return &TTY{File: f}, nil
}
func (t *TTY) Raw() error {
	state, err := term.MakeRaw(int(t.File.Fd()))
	if err == nil {
		t.saved = state
	}
	return err
}
func (t *TTY) Restore() error {
	if t.saved == nil {
		return nil
	}
	return term.Restore(int(t.File.Fd()), t.saved)
}
func (t *TTY) Size() (cols, rows int) {
	c, r, err := term.GetSize(int(t.File.Fd()))
	if err != nil || c <= 0 || r <= 0 {
		return 80, 24
	}
	return c, r
}
func (t *TTY) Close() error {
	err := t.Restore()
	closeErr := t.File.Close()
	if err != nil {
		return err
	}
	return closeErr
}

const (
	EnterAltScreen  = "\x1b[?1049h"
	ExitAltScreen   = "\x1b[?1049l"
	HideCursor      = "\x1b[?25l"
	ShowCursor      = "\x1b[?25h"
	ClearHome       = "\x1b[2J\x1b[H"
	Home            = "\x1b[H"
	MouseOn         = "\x1b[?1000h\x1b[?1006h"
	MouseOff        = "\x1b[?1000l\x1b[?1006l"
	ResetOuterModes = "\x1b[<u\x1b[>4;0m\x1b[?2004l\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1004l\x1b[?1006l\x1b[?7h\x1b[?25h"
)
