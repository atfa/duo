package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/atfa/duo/internal/xduo"
	"github.com/gdamore/tcell/v3"
)

func TestCopyCodeOmitsHyphen(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux clipboard utility fixture")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "clipboard")
	if err := os.WriteFile(filepath.Join(dir, "wl-copy"), []byte("#!/bin/sh\n/bin/cat > \"$XDUO_TEST_CLIPBOARD\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("WAYLAND_DISPLAY", "test")
	t.Setenv("XDUO_TEST_CLIPBOARD", target)
	p := panel{selected: -1, server: &xduo.WebServer{URL: "http://127.0.0.1:8787/", AccessCode: "ABCD-E7K9"}}
	s := tcell.NewSimulationScreen("")
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	defer s.Fini()
	s.SetSize(160, 36)
	p.draw(s)
	if p.copyCodeRect.Empty() {
		t.Fatal("copy button missing")
	}
	p.copyAccessCode(s)
	if got := string(s.GetClipboardData()); got != "ABCDE7K9" {
		t.Fatal(got)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "ABCDE7K9" {
		t.Fatal(string(data), err)
	}
	p.server = nil
	p.draw(s)
	if !p.copyCodeRect.Empty() {
		t.Fatal("invisible copy button remains clickable")
	}
}
