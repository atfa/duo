//go:build !windows

package xduo

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/creack/pty"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestBrowserTerminalLifecycle(t *testing.T) {
	tm, err := FindTmux()
	if err != nil {
		t.Skip(err)
	}
	tm.Socket = "xduo-web-" + filepath.Base(t.TempDir())
	ctx := context.Background()
	defer tm.run(ctx, "kill-server")
	// Stand in for the existing manager; the web bridge must attach, not launch
	// another manager, and preserve this session after a tab or server closes.
	if err := tm.run(ctx, "new-session", "-d", "-s", "xduo-manager", "sh", "-c", `printf 'WEB_READY\n'; while IFS= read -r line; do printf 'ECHO:%s\n' "$line"; done`); err != nil {
		t.Fatal(err)
	}
	if err := tm.run(ctx, "set-option", "-t", "xduo-manager", "status", "on"); err != nil {
		t.Fatal(err)
	}
	server, err := listenWeb(tm, "127.0.0.1:0", "")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	access, _ := url.Parse(server.URL)
	base := "http://" + access.Host
	if access.Fragment != "" || access.RawQuery != "" {
		t.Fatal("local URL requires a token", server.URL)
	}
	for _, path := range []string{"/", "/vendor/xterm.js", "/vendor/addon-fit.js", "/vendor/xterm.css", "/app.js"} {
		res, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 200 || len(data) == 0 {
			t.Fatalf("asset %s: %d", path, res.StatusCode)
		}
	}
	wsURL := "ws://" + access.Host + "/ws"
	for _, test := range []struct{ url, origin string }{{wsURL, ""}, {wsURL, "http://untrusted.example"}} {
		conn, res, err := websocket.DefaultDialer.Dial(test.url, http.Header{"Origin": {test.origin}})
		if conn != nil {
			conn.Close()
		}
		if err == nil || res == nil || res.StatusCode != 403 {
			t.Fatalf("untrusted connection accepted: %v", err)
		}
		res.Body.Close()
	}
	for _, header := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"} {
		headers := http.Header{"Origin": {base}, header: {"external"}}
		conn, res, err := websocket.DefaultDialer.Dial(wsURL, headers)
		if conn != nil {
			conn.Close()
		}
		if err == nil || res == nil || res.StatusCode != 401 {
			t.Fatalf("proxy inherited local access via %s", header)
		}
		res.Body.Close()
	}
	dial := func() *websocket.Conn {
		t.Helper()
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Origin": {base}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		if err := conn.WriteJSON(map[string]int{"cols": 100, "rows": 30}); err != nil {
			t.Fatal(err)
		}
		return conn
	}
	conn := dial()
	readUntil := func(c *websocket.Conn, want string) {
		t.Helper()
		c.SetReadDeadline(time.Now().Add(3 * time.Second))
		var output strings.Builder
		for {
			_, data, err := c.ReadMessage()
			if err != nil {
				t.Fatalf("waiting for %q: %v; output %q", want, err, output.String())
			}
			output.Write(data)
			if strings.Contains(output.String(), want) {
				return
			}
		}
	}
	readUntil(conn, "WEB_READY")
	second, res, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Origin": {base}})
	if second != nil {
		second.Close()
	}
	if err == nil || res == nil || res.StatusCode != 409 {
		t.Fatal("second controller accepted")
	}
	res.Body.Close()
	if err := conn.WriteMessage(websocket.BinaryMessage, []byte("hello\r")); err != nil {
		t.Fatal(err)
	}
	readUntil(conn, "ECHO:hello")
	if err := conn.WriteJSON(map[string]int{"cols": 110, "rows": 35}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		out, err := tm.execute(ctx, "display-message", "-p", "-t", "xduo-manager", "#{window_width}x#{window_height}")
		if err == nil && strings.TrimSpace(string(out)) == "110x34" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("resize not propagated: %q %v", out, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	conn.Close()
	for {
		server.mu.Lock()
		idle := server.client == nil
		server.mu.Unlock()
		if idle {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("disconnect did not release client")
		}
		time.Sleep(10 * time.Millisecond)
	}
	alive, err := tm.Alive("xduo-manager")
	if err != nil || !alive {
		t.Fatal("tab close killed manager", err)
	}
	conn = dial()
	readUntil(conn, "WEB_READY")
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(time.Second))
	for {
		if _, _, err = conn.ReadMessage(); err != nil {
			break
		}
	}
	alive, err = tm.Alive("xduo-manager")
	if err != nil || !alive {
		t.Fatal("server stop killed manager", err)
	}
	if _, err = http.Get(base); err == nil {
		t.Fatal("server still listening")
	}
}

func TestBrowserControlsFollowItsTmuxSession(t *testing.T) {
	tm, err := FindTmux()
	if err != nil {
		t.Skip(err)
	}
	tm.Socket = "xduo-context-" + filepath.Base(t.TempDir())
	ctx := context.Background()
	defer tm.run(ctx, "kill-server")
	for _, session := range []string{"xduo-manager", "xduo-example"} {
		if err := tm.run(ctx, "new-session", "-d", "-s", session, "sleep", "30"); err != nil {
			t.Fatal(err)
		}
	}
	server, err := listenWeb(tm, "127.0.0.1:0", "")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	access, _ := url.Parse(server.URL)
	conn, _, err := websocket.DefaultDialer.Dial("ws://"+access.Host+"/ws", http.Header{"Origin": {"http://" + access.Host}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteJSON(map[string]int{"cols": 100, "rows": 30}); err != nil {
		t.Fatal(err)
	}
	expect := func(want bool) {
		t.Helper()
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		for {
			kind, data, err := conn.ReadMessage()
			if err != nil {
				t.Fatal(err)
			}
			if kind != websocket.TextMessage {
				continue
			}
			var state struct {
				Type string `json:"type"`
				Duo  bool   `json:"duo"`
			}
			if err := json.Unmarshal(data, &state); err != nil {
				t.Fatal(err)
			}
			if state.Type == "session" && state.Duo == want {
				return
			}
		}
	}
	expect(false)
	// Follow the attached browser into Duo and back to the manager.
	if err := tm.run(ctx, "switch-client", "-t", "=xduo-example"); err != nil {
		t.Fatal(err)
	}
	expect(true)
	if err := tm.run(ctx, "switch-client", "-t", "=xduo-manager"); err != nil {
		t.Fatal(err)
	}
	expect(false)
}

func TestBrowserLocksLocalTmuxClient(t *testing.T) {
	tm, err := FindTmux()
	if err != nil {
		t.Skip(err)
	}
	tm.Socket = "xduo-lock-" + filepath.Base(t.TempDir())
	ctx := context.Background()
	defer tm.run(ctx, "kill-server")
	if err := tm.run(ctx, "new-session", "-d", "-s", "xduo-manager", "sleep", "30"); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(tm.Path, "-L", tm.Socket, "attach-session", "-t", "xduo-manager")
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "TMUX=") && !strings.HasPrefix(e, "TERM=") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "TERM=xterm-256color")
	tty, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 100, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { tty.Write([]byte("\n")); cmd.Process.Kill(); tty.Close(); cmd.Wait() }()
	until := time.Now().Add(3 * time.Second)
	for {
		out, _ := tm.execute(ctx, "list-clients", "-F", "#{client_pid}")
		if strings.Contains(string(out), fmt.Sprint(cmd.Process.Pid)) {
			break
		}
		if time.Now().After(until) {
			t.Fatal("local client did not attach")
		}
		time.Sleep(10 * time.Millisecond)
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "locked")
	locker := filepath.Join(dir, "lock-client")
	script := "#!/bin/sh\nprintf '%s' \"$XDUO_LOCK_SERVER\" > '" + marker + "'\nread reply\n"
	if err := os.WriteFile(locker, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	server, err := listenWeb(tm, "127.0.0.1:0", locker)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	access, _ := url.Parse(server.URL)
	conn, _, err := websocket.DefaultDialer.Dial("ws://"+access.Host+"/ws", http.Header{"Origin": {"http://" + access.Host}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteJSON(map[string]int{"cols": 100, "rows": 30}); err != nil {
		t.Fatal(err)
	}
	for {
		data, err := os.ReadFile(marker)
		if err == nil && string(data) == server.URL {
			break
		}
		if time.Now().After(until) {
			t.Fatal("local client did not run lock screen", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		kind, _, err := conn.ReadMessage()
		if err != nil {
			t.Fatal("browser was locked", err)
		}
		if kind == websocket.TextMessage {
			break
		}
	}
	// A refresh must reconnect while the local client remains on its lock screen.
	conn.Close()
	for {
		server.mu.Lock()
		idle := server.client == nil
		server.mu.Unlock()
		if idle {
			break
		}
		if time.Now().After(until) {
			t.Fatal("old browser did not detach")
		}
		time.Sleep(10 * time.Millisecond)
	}
	conn, _, err = websocket.DefaultDialer.Dial("ws://"+access.Host+"/ws", http.Header{"Origin": {"http://" + access.Host}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteJSON(map[string]int{"cols": 100, "rows": 30}); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		kind, _, err := conn.ReadMessage()
		if err != nil {
			t.Fatal("reconnect failed with locked local client", err)
		}
		if kind == websocket.TextMessage {
			break
		}
	}

}
