package agent

import (
	"io"
	"sort"
	"strings"
	"unicode/utf16"
)

// terminal is the duplex pseudo-terminal an agent process is attached to.
//
// Unix and Windows offer different primitives — one PTY file descriptor against a
// ConPTY with separate input and output pipes — but Core only ever reads, writes
// and resizes one, so this is the whole surface it needs.
type terminal interface {
	io.ReadWriteCloser
	// Resize applies a new character-cell size to the terminal.
	Resize(cols, rows int) error
}

// runningProcess is one started agent process together with its terminal.
//
// The platform files implement it because "ask the process to stop" and "kill the
// process tree" are genuine platform differences: a signal to a process group on
// Unix, a Ctrl+C byte into the pseudo console plus a job object on Windows.
type runningProcess interface {
	term() terminal
	// wait blocks until the process exits. A non-zero exit is reported as an error,
	// because Core calls it a failure unless it asked for the stop itself.
	wait() error
	// interrupt asks the process to exit and lets it clean up.
	interrupt()
	// kill ends the process and everything it started, without asking.
	kill()
	// close releases the platform handles once wait has returned.
	close()
}

// environmentBlock renders env the way CreateProcess expects it: NUL-separated
// NAME=VALUE entries, sorted by name, with a duplicate name resolved in favour of
// the later value and the whole block ending in an empty entry.
func environmentBlock(env []string) ([]uint16, error) {
	type entry struct {
		name  string
		value string
	}
	positions := make(map[string]int, len(env))
	entries := make([]entry, 0, len(env))
	for _, value := range env {
		name, _, ok := strings.Cut(value, "=")
		if !ok || name == "" || strings.ContainsRune(value, 0) {
			continue
		}
		key := strings.ToUpper(name)
		if i, seen := positions[key]; seen {
			entries[i] = entry{name: key, value: value}
			continue
		}
		positions[key] = len(entries)
		entries = append(entries, entry{name: key, value: value})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })

	text := make([]string, 0, len(entries))
	for _, e := range entries {
		text = append(text, e.value)
	}
	return utf16.Encode([]rune(strings.Join(text, "\x00") + "\x00\x00")), nil
}
