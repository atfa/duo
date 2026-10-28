//go:build !windows

package xduo

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

// Text messages carry resize controls; binary messages are literal terminal
// input/output bytes. Each browser owns one disposable tmux attach process.
func bridgeWebTerminal(tm Tmux, ws *websocket.Conn) {
	ws.SetReadLimit(1 << 20)
	_ = ws.SetReadDeadline(time.Now().Add(10 * time.Second))
	kind, data, err := ws.ReadMessage()
	var size struct {
		Cols uint16 `json:"cols"`
		Rows uint16 `json:"rows"`
	}
	if err != nil || kind != websocket.TextMessage || json.Unmarshal(data, &size) != nil || size.Cols < 2 || size.Rows < 2 || size.Cols > 1000 || size.Rows > 500 {
		return
	}
	_ = ws.SetReadDeadline(time.Time{})
	args := []string{}
	if tm.Socket != "" {
		args = append(args, "-L", tm.Socket)
	} else if fields := strings.Split(os.Getenv("TMUX"), ","); len(fields) >= 3 {
		// Pin the existing server before clearing TMUX for this outer client.
		args = append(args, "-S", strings.Join(fields[:len(fields)-2], ","))
	}
	args = append(args, "-T", "RGB", "attach-session", "-t", "=xduo-manager")
	cmd := exec.Command(tm.Path, args...)
	// This is a new outer terminal, even when the server runs inside tmux.
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "TMUX=") && !strings.HasPrefix(e, "TERM=") && !strings.HasPrefix(e, "TMUX_PANE=") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "TERM=xterm-256color")
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: size.Cols, Rows: size.Rows})
	if err != nil {
		_ = ws.WriteMessage(websocket.BinaryMessage, []byte(fmt.Sprintf("Cannot attach xduo: %v\r\n", err)))
		return
	}

	// One WebSocket reader and one writer, as required by gorilla/websocket.
	outputDone := make(chan struct{})
	go func() {
		defer close(outputDone)
		defer ws.Close()
		buf := make([]byte, 32*1024)
		for {
			n, err := terminal.Read(buf)
			if n > 0 {
				_ = ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if ws.WriteMessage(websocket.BinaryMessage, buf[:n]) != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	defer func() { cmd.Process.Kill(); terminal.Close(); <-outputDone; cmd.Wait() }()
	for {
		kind, data, err := ws.ReadMessage()
		if err != nil {
			return
		}
		switch kind {
		case websocket.BinaryMessage:
			if _, err := terminal.Write(data); err != nil {
				return
			}
		case websocket.TextMessage:
			if json.Unmarshal(data, &size) != nil || size.Cols < 2 || size.Rows < 2 || size.Cols > 1000 || size.Rows > 500 {
				return
			}
			if pty.Setsize(terminal, &pty.Winsize{Cols: size.Cols, Rows: size.Rows}) != nil {
				return
			}
		}
	}
}
