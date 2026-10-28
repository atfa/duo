package terminal

import (
	"bytes"
	"os"
	"os/exec"
	"runtime"
)

// WriteClipboard writes text using the platform clipboard utility.
func WriteClipboard(text string) error {
	switch runtime.GOOS {
	case "darwin":
		cmd := exec.Command("pbcopy")
		cmd.Stdin = bytes.NewBufferString(text)
		return cmd.Run()
	case "linux":
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			cmd := exec.Command("wl-copy")
			cmd.Stdin = bytes.NewBufferString(text)
			return cmd.Run()
		}
		cmd := exec.Command("xclip", "-selection", "clipboard")
		cmd.Stdin = bytes.NewBufferString(text)
		if err := cmd.Run(); err == nil {
			return nil
		}
		cmd = exec.Command("xsel", "--clipboard", "--input")
		cmd.Stdin = bytes.NewBufferString(text)
		return cmd.Run()
	}
	return nil
}
