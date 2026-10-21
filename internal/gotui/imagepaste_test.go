package gotui

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v3"
)

func clipboardEnv(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func commandNames(commands []clipboardCommand) []string {
	names := make([]string, len(commands))
	for i, command := range commands {
		names[i] = command.name
	}
	return names
}

func TestClipboardImageCommands(t *testing.T) {
	cases := []struct {
		name string
		goos string
		env  map[string]string
		want []string
	}{
		{name: "macOS", goos: "darwin", want: []string{"pngpaste"}},
		{
			name: "Wayland before X11",
			goos: "linux",
			env:  map[string]string{"WAYLAND_DISPLAY": "wayland-0", "DISPLAY": ":0"},
			want: []string{"wl-paste", "xclip"},
		},
		{name: "X11 only", goos: "linux", env: map[string]string{"DISPLAY": ":0"}, want: []string{"xclip"}},
		{name: "headless Linux", goos: "linux", want: nil},
		{name: "unsupported platform", goos: "freebsd", env: map[string]string{"DISPLAY": ":0"}, want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			commands := clipboardImageCommands(tc.goos, clipboardEnv(tc.env))
			got := commandNames(commands)
			if len(got) != len(tc.want) {
				t.Fatalf("commands = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("commands = %v, want %v", got, tc.want)
				}
			}
		})
	}

	commands := clipboardImageCommands("darwin", clipboardEnv(nil))
	if len(commands) != 1 || len(commands[0].args) != 1 || commands[0].args[0] != "-" {
		t.Fatalf("pngpaste command = %+v, want pngpaste -", commands)
	}
}

func TestReadClipboardImageFallsBackAndRejectsNonPNG(t *testing.T) {
	png := append([]byte{}, pngSignature...)
	png = append(png, "payload"...)
	commands := []clipboardCommand{{name: "first"}, {name: "second"}}

	got, err := readClipboardImage(commands, func(_ context.Context, command clipboardCommand) ([]byte, error) {
		if command.name == "first" {
			return nil, exec.ErrNotFound
		}
		return png, nil
	})
	if err != nil {
		t.Fatalf("fallback failed: %v", err)
	}
	if !bytes.Equal(got, png) {
		t.Fatalf("image = %q, want the PNG from the second command", got)
	}

	_, err = readClipboardImage(commands, func(_ context.Context, command clipboardCommand) ([]byte, error) {
		if command.name == "first" {
			return []byte("some text"), nil
		}
		return []byte{}, nil
	})
	if err == nil || !strings.Contains(err.Error(), "not PNG") || !strings.Contains(err.Error(), "no image data") {
		t.Fatalf("non-image error = %v, want both diagnostics", err)
	}

	_, err = readClipboardImage([]clipboardCommand{{name: "xclip"}}, func(context.Context, clipboardCommand) ([]byte, error) {
		return nil, errors.New("exit status 1: no image on clipboard")
	})
	if err == nil || !strings.Contains(err.Error(), "xclip: exit status 1") {
		t.Fatalf("command error = %v, want the stderr diagnostic", err)
	}

	if _, err := readClipboardImage(nil, nil); err == nil || !strings.Contains(err.Error(), "no graphical clipboard") {
		t.Fatalf("headless error = %v", err)
	}
}

func TestWritePastedImageUsesAPrivateTemporaryFile(t *testing.T) {
	png := append([]byte{}, pngSignature...)
	png = append(png, "payload"...)
	path, err := writePastedImage(png, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("temporary image path = %q, want absolute", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("temporary image mode = %o, want 600", perm)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, png) {
		t.Fatalf("temporary image = %q, want the clipboard bytes", data)
	}
}

func TestPasteImageInsertsThePathAndPointsAtNativeAttachOnFailure(t *testing.T) {
	app := New(newFakeSession())
	app.pasteImage = func() (string, error) { return "/tmp/duo image.png", nil }
	pressText(t, app, "look ")
	app.handleKey(tcell.NewEventKey(tcell.KeyCtrlV, "", tcell.ModNone))
	if got, want := app.composer.Text(), "look `/tmp/duo image.png` "; got != want {
		t.Fatalf("composer after Ctrl+V = %q, want %q", got, want)
	}
	if app.statusErr || !strings.Contains(app.status, "pasted image path") {
		t.Fatalf("status after a successful paste = %q (error=%t)", app.status, app.statusErr)
	}

	app2 := New(newFakeSession())
	app2.pasteImage = func() (string, error) { return "", errors.New("no PNG image on the clipboard") }
	app2.handleKey(tcell.NewEventKey(tcell.KeyCtrlV, "", tcell.ModNone))
	if app2.composer.Text() != "" {
		t.Fatalf("a failed paste changed the composer: %q", app2.composer.Text())
	}
	if !app2.statusErr || !strings.Contains(app2.status, "Ctrl+A") || !strings.Contains(app2.status, "Ctrl+T") {
		t.Fatalf("failure status = %q (error=%t), want native-attach guidance", app2.status, app2.statusErr)
	}
}
