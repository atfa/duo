//go:build !windows

package xduo

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

// Client text messages carry resize controls; server text messages report the
// active session. Binary messages carry literal terminal input/output bytes.
// Each browser owns one disposable tmux attach process.
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

	// PTY output and session metadata share one serialized WebSocket writer.
	var writer sync.Mutex
	send := func(kind int, data []byte) error {
		writer.Lock()
		defer writer.Unlock()
		_ = ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return ws.WriteMessage(kind, data)
	}
	ctx, cancel := context.WithCancel(context.Background())
	sessionDone := make(chan struct{})
	go func() {
		defer close(sessionDone)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		last := ""
		for {
			// Identify this browser's client, not another local tmux client.
			out, err := tm.execute(ctx, "list-clients", "-F", "#{client_pid}\t#{session_name}")
			current := ""
			if err == nil {
				for _, line := range strings.Split(string(out), "\n") {
					pid, name, ok := strings.Cut(line, "\t")
					if ok && pid == strconv.Itoa(cmd.Process.Pid) {
						current = name
						break
					}
				}
			}
			if current != last || last == "" {
				duo := current != "xduo-manager" && strings.HasPrefix(current, "xduo-")
				data, _ := json.Marshal(struct {
					Type string `json:"type"`
					Duo  bool   `json:"duo"`
				}{"session", duo})
				if send(websocket.TextMessage, data) != nil {
					return
				}
				last = current
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	// One WebSocket reader and one writer, as required by gorilla/websocket.
	outputDone := make(chan struct{})
	go func() {
		defer close(outputDone)
		defer ws.Close()
		buf := make([]byte, 32*1024)
		for {
			n, err := terminal.Read(buf)
			if n > 0 {
				if send(websocket.BinaryMessage, buf[:n]) != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	defer func() {
		cancel()
		cmd.Process.Kill()
		terminal.Close()
		<-outputDone
		<-sessionDone
		cmd.Wait()
	}()
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
