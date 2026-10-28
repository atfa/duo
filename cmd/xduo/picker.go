package main

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"

	"github.com/atfa/duo/internal/xduo"
	"github.com/gdamore/tcell/v3"
	rw "github.com/mattn/go-runewidth"
	ui "github.com/metaspartan/gotui/v5"
	"github.com/metaspartan/gotui/v5/widgets"
)

type directoryPicker struct {
	cwd                   string
	dirs                  []string
	recent                []string
	focus, cursor, scroll int
	hits                  map[int]image.Rectangle
	error                 string
}

func (p *panel) openPicker() {
	cwd, _ := os.Getwd()
	p.picker = directoryPicker{cwd: cwd}
	data, err := os.ReadFile(filepath.Join(p.dir, "recent-projects.json"))
	if err == nil {
		if err = json.Unmarshal(data, &p.picker.recent); err != nil {
			p.picker.error = err.Error()
		}
	} else if !os.IsNotExist(err) {
		p.picker.error = err.Error()
	}
	if len(p.picker.recent) > 10 {
		p.picker.recent = p.picker.recent[:10]
	}
	p.mode = "input"
	p.input = ""
	p.readDirectories()
}
func (p *panel) readDirectories() {
	entries, err := os.ReadDir(p.picker.cwd)
	p.picker.dirs = []string{".."}
	p.picker.cursor = 0
	p.picker.scroll = 0
	if err != nil {
		p.picker.error = err.Error()
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			p.picker.dirs = append(p.picker.dirs, e.Name())
		}
	}
}
func (p *panel) remember(path string) error {
	recent := []string{path}
	data, err := os.ReadFile(filepath.Join(p.dir, "recent-projects.json"))
	var old []string
	if err == nil {
		if err = json.Unmarshal(data, &old); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	for _, v := range old {
		if v != path && len(recent) < 10 {
			recent = append(recent, v)
		}
	}
	data, err = json.MarshalIndent(recent, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(p.dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(p.dir, ".recent-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(p.dir, "recent-projects.json"))
}
func (p *panel) createPicked() {
	path := p.input
	if path == "" {
		path = p.picker.cwd
	}
	if path == "" {
		path = "."
	}
	i, err := xduo.Create(context.Background(), p.tmux, p.dir, path, p.duo)
	if err != nil {
		p.picker.error = err.Error()
		p.message = err.Error()
		return
	}
	p.mode = ""
	p.refresh()
	p.selected = len(p.rows) - 1
	p.message = "Instance started."
	if err = p.remember(i.Project); err != nil {
		p.message = "Instance started; history: " + err.Error()
	}
}
func (p *panel) pickerKey(e *tcell.EventKey) {
	d := &p.picker
	switch e.Key() {
	case tcell.KeyEsc:
		p.mode = ""
		return
	case tcell.KeyTab:
		d.focus = (d.focus + 1) % 5
		return
	case tcell.KeyBacktab:
		d.focus = (d.focus + 4) % 5
		return
	case tcell.KeyUp, tcell.KeyDown:
		delta := 1
		if e.Key() == tcell.KeyUp {
			delta = -1
		}
		if d.focus == 0 {
			d.focus = 1
		}
		n := len(d.dirs)
		if d.focus == 2 {
			n = len(d.recent)
		}
		d.cursor = max(0, min(n-1, d.cursor+delta))
		return
	case tcell.KeyEnter:
		switch d.focus {
		case 1:
			if len(d.dirs) > 0 {
				d.cwd = filepath.Clean(filepath.Join(d.cwd, d.dirs[min(d.cursor, len(d.dirs)-1)]))
				p.input = ""
				p.readDirectories()
			}
		case 2:
			if len(d.recent) > 0 {
				d.cwd = d.recent[min(d.cursor, len(d.recent)-1)]
				p.input = ""
				p.readDirectories()
				d.focus = 3
			}
		case 4:
			p.mode = ""
		default:
			p.createPicked()
		}
		return
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if d.focus == 0 {
			r := []rune(p.input)
			if len(r) > 0 {
				p.input = string(r[:len(r)-1])
			}
		}
	case tcell.KeyRune:
		if d.focus == 0 {
			p.input += e.Str()
		}
	}
}
func (p *panel) drawPicker(s tcell.Screen) {
	p.picker.hits = nil
	w, h := s.Size()
	width, height := min(w-4, 90), min(h-4, 33)
	if width < 36 || height < 22 {
		s.Clear()
		s.PutStrStyled(1, 1, "Enlarge terminal · Esc cancels", accent)
		return
	}
	x, y := (w-width)/2, (h-height)/2
	d := &p.picker
	d.hits = map[int]image.Rectangle{}
	// Subdue the dashboard while keeping the workspace context visible.
	for yy := 0; yy < h; yy++ {
		for xx := 0; xx < w; xx++ {
			c, _, _ := s.Get(xx, yy)
			s.PutStrStyled(xx, yy, c, dim)
		}
	}
	fill := func(r image.Rectangle, style tcell.Style) {
		for yy := r.Min.Y; yy < r.Max.Y; yy++ {
			s.PutStrStyled(r.Min.X, yy, strings.Repeat(" ", r.Dx()), style)
		}
	}
	fill(image.Rect(x+2, y+1, x+width+2, y+height+1), shadow)
	fill(image.Rect(x, y, x+width, y+height), surface)
	box := widgets.NewParagraph()
	box.SetRect(x, y, x+width, y+height)
	buf := ui.NewBuffer(image.Rect(0, 0, w, h))
	box.Draw(buf)
	for yy := y; yy < y+height; yy++ {
		for xx := x; xx < x+width; xx++ {
			c := buf.GetCell(image.Pt(xx, yy))
			if c.Rune != ' ' && c.Rune != 0 {
				s.PutStrStyled(xx, yy, string(c.Rune), borderStyle)
			}
		}
	}
	text := func(row int, label string, style tcell.Style) {
		s.PutStrStyled(x+3, row, rw.Truncate(label, width-6, "…"), style)
	}
	text(y+1, "＋  New workspace", surfaceAccent)
	text(y+2, "Choose a Git project to start a Duo session", surfaceMuted)
	text(y+4, "CURRENT DIRECTORY", surfaceMuted)
	text(y+5, d.cwd, surface)
	field := image.Rect(x+3, y+7, x+width-3, y+8)
	fieldStyle := fieldBase
	if d.focus == 0 {
		fieldStyle = selected
	}
	fill(field, fieldStyle)
	label := "Path: " + p.input
	if p.input == "" {
		label = "Path: type a directory, or browse below"
	}
	s.PutStrStyled(field.Min.X+1, field.Min.Y, rw.Truncate(label, field.Dx()-2, "…"), fieldStyle)
	d.hits[0] = field
	if d.focus == 0 {
		s.ShowCursor(field.Min.X+min(field.Dx()-2, 7+rw.StringWidth(p.input)), field.Min.Y)
	} else {
		s.HideCursor()
	}
	count := max(2, min(5, height-22))
	text(y+9, fmt.Sprintf("BROWSE DIRECTORIES  ·  %d folders", max(0, len(d.dirs)-1)), surfaceMuted)
	if d.focus == 1 {
		if d.cursor < d.scroll {
			d.scroll = d.cursor
		}
		if d.cursor >= d.scroll+count {
			d.scroll = d.cursor - count + 1
		}
	}
	drawItem := func(row, id int, label string, active bool) {
		r := image.Rect(x+3, row, x+width-3, row+1)
		style := surface
		mark := "  "
		if active {
			style = selected
			mark = "› "
		}
		fill(r, style)
		s.PutStrStyled(r.Min.X+1, row, rw.Truncate(mark+label, r.Dx()-2, "…"), style)
		d.hits[id] = r
	}
	for j := 0; j < count && j+d.scroll < len(d.dirs); j++ {
		i := j + d.scroll
		name := d.dirs[i]
		if name == ".." {
			name = "..  Parent directory"
		}
		drawItem(y+10+j, 100+i, name+" /", d.focus == 1 && d.cursor == i)
	}
	recentY := y + 11 + count
	text(recentY, fmt.Sprintf("RECENT PROJECTS  ·  %d / 10", len(d.recent)), surfaceMuted)
	available := max(1, y+height-6-recentY)
	start := 0
	if d.focus == 2 {
		start = max(0, d.cursor-available+1)
	}
	if len(d.recent) == 0 {
		text(recentY+1, "Your recently opened projects will appear here", surfaceMuted)
	}
	for j := 0; j < available && j+start < len(d.recent); j++ {
		i := j + start
		drawItem(recentY+1+j, 200+i, d.recent[i], d.focus == 2 && d.cursor == i)
	}
	if d.error != "" {
		text(y+height-4, d.error, errorStyle)
	} else {
		text(y+height-4, "Tab switch section   ↑↓ select   Enter open   Esc cancel", surfaceMuted)
	}
	buttonY := y + height - 2
	cancel := image.Rect(x+width-34, buttonY, x+width-22, buttonY+1)
	confirm := image.Rect(x+width-20, buttonY, x+width-3, buttonY+1)
	cancelStyle := fieldBase
	if d.focus == 4 {
		cancelStyle = selected
	}
	fill(cancel, cancelStyle)
	s.PutStrStyled(cancel.Min.X+1, buttonY, " Cancel ", cancelStyle)
	primary := primaryButton
	if d.focus == 3 {
		primary = primary.Bold(true)
	}
	fill(confirm, primary)
	s.PutStrStyled(confirm.Min.X+1, buttonY, " Create Duo  ↵", primary)
	d.hits[3] = confirm
	d.hits[4] = cancel
}
func (p *panel) pickerClick(x, y int) {
	for id, r := range p.picker.hits {
		if image.Pt(x, y).In(r) {
			switch {
			case id >= 200:
				p.picker.focus = 2
				p.picker.cursor = id - 200
			case id >= 100:
				p.picker.focus = 1
				p.picker.cursor = id - 100
			default:
				p.picker.focus = id
			}
			if id != 0 {
				p.pickerKey(tcell.NewEventKey(tcell.KeyEnter, "", tcell.ModNone))
			}
			return
		}
	}
}
