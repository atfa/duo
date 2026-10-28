package xduo

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNamedTunnelLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell process fixture")
	}
	dir := t.TempDir()
	credentials := filepath.Join(dir, "credentials with spaces.json")
	if err := os.WriteFile(credentials, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	config, _ := json.Marshal(map[string]string{"hostname": "desktop.example", "tunnel": "test-id", "credentialsFile": credentials})
	if err := os.WriteFile(filepath.Join(dir, "tunnel.json"), config, 0600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf 'Registered tunnel connection\\n' >&2\nexec /bin/sleep 30\n"
	if err := os.WriteFile(filepath.Join(dir, "cloudflared"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	tunnel, err := StartTunnel(dir, "http://127.0.0.1:12345/", "/random-entry")
	if err != nil {
		t.Fatal(err)
	}
	defer tunnel.Close()
	for until := time.Now().Add(3 * time.Second); !tunnel.Ready(); {
		if time.Now().After(until) {
			t.Fatal("tunnel never became ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if tunnel.URL != "https://desktop.example/random-entry" {
		t.Fatal(tunnel.URL)
	}
	yaml, err := os.ReadFile(filepath.Join(dir, "tunnel.yml"))
	if err != nil || !strings.Contains(string(yaml), `service: "http://127.0.0.1:12345"`) || !strings.Contains(string(yaml), `credentials with spaces.json`) {
		t.Fatal(string(yaml), err)
	}
	if err := tunnel.Close(); err != nil || tunnel.Running() {
		t.Fatal("tunnel stayed running", err)
	}
}
