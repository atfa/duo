package main

import (
	"encoding/json"
	"fmt"
	"github.com/gdamore/tcell/v3"
	"os"
	"path/filepath"
	"testing"
)

func TestPickerBrowseHistoryAndCancel(t *testing.T) {
	p := panel{dir: t.TempDir()}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "child"), 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		if err := p.remember(fmt.Sprintf("/project/%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.remember("/project/5"); err != nil {
		t.Fatal(err)
	}
	p.openPicker()
	if len(p.picker.recent) != 10 || p.picker.recent[0] != "/project/5" {
		t.Fatal(p.picker.recent)
	}
	p.picker.cwd = root
	p.readDirectories()
	p.picker.focus = 1
	p.picker.cursor = 1
	p.pickerKey(tcell.NewEventKey(tcell.KeyEnter, "", tcell.ModNone))
	if p.picker.cwd != filepath.Join(root, "child") {
		t.Fatal(p.picker.cwd)
	}
	p.pickerKey(tcell.NewEventKey(tcell.KeyEnter, "", tcell.ModNone))
	if p.picker.cwd != root {
		t.Fatal(p.picker.cwd)
	}
	s := tcell.NewSimulationScreen("")
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	defer s.Fini()
	s.SetSize(100, 36)
	p.draw(s)
	r := p.picker.hits[4]
	p.pickerClick(r.Min.X, r.Min.Y)
	if p.mode != "" {
		t.Fatal("cancel did not close picker")
	}
	data, err := os.ReadFile(filepath.Join(p.dir, "recent-projects.json"))
	if err != nil {
		t.Fatal(err)
	}
	var history []string
	if err = json.Unmarshal(data, &history); err != nil || len(history) != 10 {
		t.Fatal(string(data), err)
	}
}

func TestPickerVisualLayoutAndHitRegions(t *testing.T) {
	p := panel{dir: t.TempDir(), selected: -1}
	p.openPicker()
	for i := 0; i < 10; i++ {
		p.picker.recent = append(p.picker.recent, fmt.Sprintf("/work/project-%d", i))
	}
	s := tcell.NewSimulationScreen("")
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	defer s.Fini()
	s.SetSize(120, 42)
	p.draw(s)
	for i := 0; i < 10; i++ {
		if p.picker.hits[200+i].Empty() {
			t.Fatalf("recent project %d not visible", i)
		}
	}
	for id, r := range p.picker.hits {
		if r.Empty() || r.Min.X < 0 || r.Min.Y < 0 || r.Max.X > 120 || r.Max.Y > 42 {
			t.Fatalf("invalid hit region %d: %v", id, r)
		}
		for other, rect := range p.picker.hits {
			if id != other && r.Overlaps(rect) {
				t.Fatalf("overlapping controls %d and %d", id, other)
			}
		}
	}
	_, style, _ := s.Get(1, 0)
	if style != dim {
		t.Fatal("modal backdrop was not dimmed")
	}
	s.SetSize(32, 16)
	p.draw(s)
	if len(p.picker.hits) != 0 {
		t.Fatal("small-terminal fallback left invisible clickable controls")
	}
}
