package tui

import (
	"io"
	"time"
)

type renderMode int

const (
	renderNormal renderMode = iota
	renderFullClear
)

// frameInterval bounds the renderer to about 60 FPS and coalesces bursts of
// invalidations (for example a SIGWINCH storm while dragging a window) into a
// single frame.
const frameInterval = 16 * time.Millisecond

// renderer turns state invalidations into whole-frame writes. markDirty only
// arms a timer; rendering happens when the timer fires, so any number of
// changes inside one interval cost exactly one frame.
type renderer struct {
	interval time.Duration
	build    func(renderMode) string
	out      io.Writer

	dirty  bool
	full   bool
	timer  *time.Timer
	due    <-chan time.Time
	frames int
}

func newRenderer(interval time.Duration, out io.Writer, build func(renderMode) string) *renderer {
	return &renderer{interval: interval, build: build, out: out}
}

// markDirty schedules a frame; full additionally requests a full-screen clear
// for that frame (resize, layout change, alternate-screen re-entry).
func (r *renderer) markDirty(full bool) {
	r.dirty = true
	r.full = r.full || full
	if r.due != nil {
		return
	}
	if r.timer == nil {
		r.timer = time.NewTimer(r.interval)
	} else {
		if !r.timer.Stop() {
			select {
			case <-r.timer.C:
			default:
			}
		}
		r.timer.Reset(r.interval)
	}
	r.due = r.timer.C
}

// dueChan is nil while no frame is scheduled, so an idle renderer never wakes
// the event loop.
func (r *renderer) dueChan() <-chan time.Time { return r.due }

// flush draws the latest state once if a frame is scheduled and reports whether
// it wrote anything.
func (r *renderer) flush() bool {
	r.due = nil
	if !r.dirty {
		return false
	}
	mode := renderNormal
	if r.full {
		mode = renderFullClear
	}
	r.dirty = false
	r.full = false
	frame := r.build(mode)
	r.frames++
	if frame == "" || r.out == nil {
		return false
	}
	_, _ = io.WriteString(r.out, frame)
	return true
}
