package terminal

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

type TTY struct {
	File  *os.File
	saved string
}

func Open() (*TTY, error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	t := &TTY{File: f}
	saved, err := t.runStty("-g")
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	t.saved = strings.TrimSpace(saved)
	return t, nil
}

func (t *TTY) Raw() error {
	_, err := t.runStty("raw", "-echo")
	return err
}

func (t *TTY) Restore() error {
	if t.saved == "" {
		return nil
	}
	_, err := t.runStty(t.saved)
	return err
}

func (t *TTY) Size() (cols, rows int) {
	out, err := t.runStty("size")
	if err != nil {
		return 80, 24
	}
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return 80, 24
	}
	r, _ := strconv.Atoi(fields[0])
	c, _ := strconv.Atoi(fields[1])
	if c < 40 {
		c = 80
	}
	if r < 12 {
		r = 24
	}
	return c, r
}

func (t *TTY) Close() error {
	_ = t.Restore()
	return t.File.Close()
}

func (t *TTY) runStty(args ...string) (string, error) {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, shellQuote(arg))
	}
	cmd := exec.Command("sh", "-c", "stty "+strings.Join(quoted, " ")+" < /dev/tty")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("stty %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func shellQuote(v string) string { return "'" + strings.ReplaceAll(v, "'", "'\"'\"'") + "'" }

const (
	EnterAltScreen = "\x1b[?1049h"
	ExitAltScreen  = "\x1b[?1049l"
	HideCursor     = "\x1b[?25l"
	ShowCursor     = "\x1b[?25h"
	ClearHome      = "\x1b[2J\x1b[H"
	Home           = "\x1b[H"
	MouseOn        = "\x1b[?1000h\x1b[?1006h"
	MouseOff       = "\x1b[?1000l\x1b[?1006l"
	// ResetOuterModes undoes terminal modes a native Pi session can leave enabled.
	// It is intentionally separate from alternate-screen handling.
	ResetOuterModes = "\x1b[<u\x1b[>4;0m\x1b[?2004l\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1004l\x1b[?1006l\x1b[?7h\x1b[?25h"
)
