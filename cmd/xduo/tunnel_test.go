package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/xduo"
	"github.com/gdamore/tcell/v3"
)

func TestTunnelOpensBeforeStartupResult(t *testing.T) {
	p := panel{dir: t.TempDir(), selected: -1, server: &xduo.WebServer{URL: "http://127.0.0.1:12345/", EntryPath: "/entry", AccessCode: "ABCD-E7K9"}}
	p.toggleTunnel()
	defer p.cancelTunnelStart()
	if p.mode != "tunnel" || p.tunnelPending == nil {
		t.Fatal("click did not immediately open loading panel")
	}
	pending := p.tunnelPending
	p.toggleTunnel()
	if p.tunnelPending != pending {
		t.Fatal("second click duplicated startup")
	}
	s := tcell.NewSimulationScreen("")
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	defer s.Fini()
	s.SetSize(120, 36)
	p.draw(s)
	read := func() string {
		var b strings.Builder
		for y := 0; y < 36; y++ {
			for x := 0; x < 120; x++ {
				c, _, _ := s.Get(x, y)
				b.WriteString(c)
			}
		}
		return b.String()
	}
	if !strings.Contains(read(), "Loading Cloudflare Tunnel") {
		t.Fatal("loading state not visible")
	}
	if p.tunnelOKRect.Empty() || !p.tunnelCopyRect.Empty() {
		t.Fatal("loading controls incorrectly enabled")
	}
	select {
	case result := <-p.tunnelPending:
		p.finishTunnelStart(result)
	case <-time.After(3 * time.Second):
		t.Fatal("configuration result never returned")
	}
	if !errors.Is(p.tunnelError, xduo.ErrTunnelNotConfigured) {
		t.Fatal(p.tunnelError)
	}
	p.draw(s)
	if !strings.Contains(read(), xduo.TunnelSetupURL) {
		t.Fatal("missing public setup guide")
	}
	p.key(tcell.NewEventKey(tcell.KeyEnter, "", tcell.ModNone))
	if p.mode != "" {
		t.Fatal("OK did not close panel")
	}
}

func TestTunnelReadyHasWorkingRoomCopy(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("clipboard and process fixtures")
	}
	dir := t.TempDir()
	credentials := filepath.Join(dir, "credentials.json")
	os.WriteFile(credentials, []byte("{}"), 0600)
	config, _ := json.Marshal(map[string]string{"hostname": "desktop.example", "tunnel": "test-id", "credentialsFile": credentials})
	os.WriteFile(filepath.Join(dir, "tunnel.json"), config, 0600)
	if err := os.WriteFile(filepath.Join(dir, "cloudflared"), []byte("#!/bin/sh\nprintf 'Registered tunnel connection\\n' >&2\nexec /bin/sleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wl-copy"), []byte("#!/bin/sh\n/bin/cat > \"$XDUO_TEST_CLIPBOARD\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("WAYLAND_DISPLAY", "test")
	t.Setenv("XDUO_TEST_CLIPBOARD", filepath.Join(dir, "copied"))
	p := panel{dir: dir, selected: -1, server: &xduo.WebServer{URL: "http://127.0.0.1:12345/", EntryPath: "/entry", AccessCode: "ABCD-E7K9"}}
	p.toggleTunnel()
	defer p.cancelTunnelStart()
	select {
	case result := <-p.tunnelPending:
		p.finishTunnelStart(result)
	case <-time.After(3 * time.Second):
		t.Fatal("start result missing")
	}
	if p.tunnelError != nil {
		t.Fatal(p.tunnelError)
	}
	defer p.tunnel.Close()
	until := time.Now().Add(3 * time.Second)
	for !p.tunnelReady() {
		if time.Now().After(until) {
			t.Fatal("never ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	s := tcell.NewSimulationScreen("")
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	defer s.Fini()
	s.SetSize(120, 36)
	p.draw(s)
	if p.tunnelCopyRect.Empty() || p.tunnelOKRect.Empty() {
		t.Fatal("ready buttons missing")
	}
	p.key(tcell.NewEventKey(tcell.KeyTab, "", tcell.ModNone))
	p.key(tcell.NewEventKey(tcell.KeyEnter, "", tcell.ModNone))
	if !p.tunnelCopyRequested || p.mode != "tunnel" {
		t.Fatal("copy should leave dialog open")
	}
	p.copyTunnelCode(s)
	if string(s.GetClipboardData()) != "ABCDE7K9" {
		t.Fatal("copied hyphenated code")
	}
	p.key(tcell.NewEventKey(tcell.KeyEsc, "", tcell.ModNone))
	if p.mode != "" || !p.tunnel.Running() {
		t.Fatal("closing dialog stopped connector")
	}
}
