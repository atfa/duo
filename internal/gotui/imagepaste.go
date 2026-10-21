package gotui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// clipboardImageTimeout bounds one external clipboard command. These commands
// normally answer immediately; a stuck one must not freeze the dashboard.
const clipboardImageTimeout = 2 * time.Second

var pngSignature = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

type clipboardCommand struct {
	name string
	args []string
}

// clipboardImageCommands lists the commands to try, in order. Wayland comes
// before X11 because a Wayland session commonly has both variables set through
// XWayland, while the Wayland clipboard is the one the user just copied from.
func clipboardImageCommands(goos string, getenv func(string) string) []clipboardCommand {
	switch goos {
	case "darwin":
		// pngpaste requires an output target; "-" means stdout.
		return []clipboardCommand{{name: "pngpaste", args: []string{"-"}}}
	case "linux":
		var commands []clipboardCommand
		if strings.TrimSpace(getenv("WAYLAND_DISPLAY")) != "" {
			commands = append(commands, clipboardCommand{name: "wl-paste", args: []string{"--type", "image/png"}})
		}
		if strings.TrimSpace(getenv("DISPLAY")) != "" {
			commands = append(commands, clipboardCommand{name: "xclip", args: []string{"-selection", "clipboard", "-t", "image/png", "-o"}})
		}
		return commands
	default:
		return nil
	}
}

// readClipboardImage tries each platform command until one returns a PNG. A
// missing tool, a text-only clipboard and a hung command are all reported
// together, so the operator can tell an installation problem from an empty
// clipboard.
func readClipboardImage(commands []clipboardCommand, run func(context.Context, clipboardCommand) ([]byte, error)) ([]byte, error) {
	if len(commands) == 0 {
		return nil, errors.New("no graphical clipboard is available")
	}
	// One budget for the whole fallback chain: a stuck Wayland clipboard must
	// not buy a second timeout from X11.
	ctx, cancel := context.WithTimeout(context.Background(), clipboardImageTimeout)
	defer cancel()
	var failures []string
	for _, command := range commands {
		data, err := run(ctx, command)
		if err != nil {
			if errors.Is(err, exec.ErrNotFound) {
				failures = append(failures, command.name+" is not installed")
			} else {
				failures = append(failures, command.name+": "+err.Error())
			}
			continue
		}
		if len(data) == 0 {
			failures = append(failures, command.name+" returned no image data")
			continue
		}
		if !bytes.HasPrefix(data, pngSignature) {
			failures = append(failures, command.name+" returned data that is not PNG")
			continue
		}
		return data, nil
	}
	return nil, fmt.Errorf("no PNG image on the clipboard (%s)", strings.Join(failures, "; "))
}

// runClipboardCommand runs one command with a timeout and keeps its stderr for
// the error message. "command not found" and "no image" must not look alike.
func runClipboardCommand(ctx context.Context, command clipboardCommand) ([]byte, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, command.name, command.args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("timed out after %s", clipboardImageTimeout)
		}
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return nil, fmt.Errorf("%w: %s", err, message)
		}
		return nil, err
	}
	return out, nil
}

// writePastedImage keeps the clipboard bytes in a private temporary PNG and
// returns its absolute path. The path is what the agent receives; the image
// never enters the text protocol.
func writePastedImage(data []byte, dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		dir = os.TempDir()
	}
	file, err := os.CreateTemp(dir, "duo-paste-*.png")
	if err != nil {
		return "", fmt.Errorf("create temporary image: %w", err)
	}
	name := file.Name()
	fail := func(err error) (string, error) {
		_ = file.Close()
		_ = os.Remove(name)
		return "", err
	}
	if err := file.Chmod(0o600); err != nil {
		return fail(fmt.Errorf("secure temporary image: %w", err))
	}
	if _, err := file.Write(data); err != nil {
		return fail(fmt.Errorf("write temporary image: %w", err))
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return "", fmt.Errorf("close temporary image: %w", err)
	}
	absolute, err := filepath.Abs(name)
	if err != nil {
		_ = os.Remove(name)
		return "", fmt.Errorf("resolve temporary image: %w", err)
	}
	return absolute, nil
}

// pasteClipboardImage is the real Ctrl+V implementation.
func pasteClipboardImage() (string, error) {
	commands := clipboardImageCommands(runtime.GOOS, os.Getenv)
	data, err := readClipboardImage(commands, runClipboardCommand)
	if err != nil {
		return "", err
	}
	return writePastedImage(data, os.TempDir())
}

// pasteImageFromClipboard inserts a path rather than pretending the terminal
// can carry pixels: the agent reads the image from the file. When no image or
// tool is available, the status points at the native attach path, where the
// agent's own UI can handle a paste.
func (a *App) pasteImageFromClipboard() {
	if a.pasteImage == nil {
		a.setStatus("clipboard image paste is unavailable; use Ctrl+A / Ctrl+T to paste in the agent's native terminal", true)
		return
	}
	path, err := a.pasteImage()
	if err != nil {
		a.setStatus("no image pasted: "+err.Error()+"; use Ctrl+A / Ctrl+T to paste in the agent's native terminal", true)
		return
	}
	path = strings.TrimSpace(path)
	if path == "" {
		a.setStatus("no image pasted; use Ctrl+A / Ctrl+T to paste in the agent's native terminal", true)
		return
	}
	a.closeMention()
	insert := path + " "
	if strings.ContainsAny(path, " \t") {
		insert = "`" + path + "` "
	}
	a.composer.InsertString(insert)
	a.resetHistory()
	a.setStatus("pasted image path "+path, false)
}
