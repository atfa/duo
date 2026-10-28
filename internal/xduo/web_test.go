//go:build !windows

package xduo

import (
	"context"
	"io"
	"net/http"
	"net/url"
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
	server, err := listenWeb(tm, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	access, _ := url.Parse(server.URL)
	base := "http://" + access.Host
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
	wsURL := "ws://" + access.Host + "/ws?token=" + access.Fragment
	for _, test := range []struct{ url, origin string }{{"ws://" + access.Host + "/ws", base}, {wsURL, "http://untrusted.example"}} {
		conn, res, err := websocket.DefaultDialer.Dial(test.url, http.Header{"Origin": {test.origin}})
		if conn != nil {
			conn.Close()
		}
		if err == nil || res == nil || res.StatusCode != 403 {
			t.Fatalf("untrusted connection accepted: %v", err)
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
