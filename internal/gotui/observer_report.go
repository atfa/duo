package gotui

import (
	"image"
	"strings"
	"time"

	"github.com/atfa/duo/internal/session"
	rw "github.com/mattn/go-runewidth"
	ui "github.com/metaspartan/gotui/v5"
)

// observerReportOverlay shows the observer's status reports and nothing else.
//
// The observer runs only when a human asks for one, so this panel is where its
// output belongs: a report is never a Timeline entry. Opening the panel asks for
// a fresh report, R asks again, and an in-flight request shows its elapsed time
// instead of looking frozen.
type observerReportOverlay struct {
	*ui.Block
	theme    theme
	box      image.Rectangle
	width    int
	offset   int
	lines    []renderLine
	observer session.ObserverView
}

func newObserverReports(t theme) *observerReportOverlay {
	return &observerReportOverlay{Block: newModalBlock(t), theme: t, width: -1}
}

func (o *observerReportOverlay) begin(observer session.ObserverView) {
	o.offset = 0
	o.observer = observer
	o.reflow()
}

// updateObserver redraws when the view changes, and keeps redrawing while a
// request is in flight so the elapsed row keeps counting. A value comparison
// alone would freeze that row, because only the clock changes.
func (o *observerReportOverlay) updateObserver(observer session.ObserverView) {
	if !observer.Analyzing && observerSame(o.observer, observer) {
		return
	}
	o.observer = observer
	o.reflow()
}

func (o *observerReportOverlay) hasReports() bool { return len(o.observer.Reports) > 0 }

// observerSame reports whether a re-render would draw the same panel. The view
// carries a slice now, so it can no longer be compared as a value.
func observerSame(a, b session.ObserverView) bool {
	if a.Enabled != b.Enabled || a.HasKey != b.HasKey || a.LastNotice != b.LastNotice ||
		a.Analyzing != b.Analyzing || len(a.Reports) != len(b.Reports) {
		return false
	}
	for i := range a.Reports {
		if a.Reports[i] != b.Reports[i] {
			return false
		}
	}
	return true
}

func (o *observerReportOverlay) reflow() {
	o.lines = nil
	width := max(1, o.Inner.Dx())
	if o.observer.LastNotice != "" {
		message := o.theme.tr("Latest observer notice") + observerNoticeTimestamp(o.observer.LastNoticeAt) + ": " + observerNoticeText(o.observer.LastNotice)
		o.lines = append(o.lines, wrapText(message, o.theme.BodyMuted, width)...)
		o.lines = append(o.lines, makeLine("", o.theme.BodyMuted, 0))
	}
	if o.observer.Analyzing {
		o.lines = append(o.lines, wrapText(o.analyzingLine(), o.theme.BodyMuted, width)...)
		o.lines = append(o.lines, makeLine("", o.theme.BodyMuted, 0))
	}
	if !o.hasReports() {
		o.lines = append(o.lines, wrapText(o.emptyMessage(), o.theme.BodyMuted, width)...)
		o.clampOffset()
		return
	}
	for _, report := range o.observer.Reports {
		o.lines = append(o.lines, makeLine(report.At.Format("15:04"),
			ui.Style{Fg: o.theme.Accent, Bg: o.theme.Panel, Modifier: ui.ModifierBold}, 0))
		o.lines = append(o.lines, wrapText(strings.TrimSpace(report.Text), o.theme.BodyMuted, width)...)
		o.lines = append(o.lines, makeLine("", o.theme.BodyMuted, 0))
	}
	o.clampOffset()
}

// maxOffset is the furthest the panel can scroll and still fill its body.
func (o *observerReportOverlay) maxOffset() int {
	return max(0, len(o.lines)-max(1, o.Inner.Dy()-1))
}

// clampOffset keeps the viewport inside the content it now holds. A re-analysis
// or a shorter history can leave an old scroll position past the end, which
// would draw an empty body until the human scrolled back up.
func (o *observerReportOverlay) clampOffset() {
	o.offset = min(max(0, o.offset), o.maxOffset())
}

// analyzingLine names the elapsed time, which is the only evidence a slow model
// is still working.
func (o *observerReportOverlay) analyzingLine() string {
	line := o.theme.tr("Asking the observer what the agents are doing right now…")
	if o.observer.RequestedAt.IsZero() {
		return line
	}
	elapsed := time.Since(o.observer.RequestedAt)
	if elapsed < 0 {
		return line
	}
	return line + " " + elapsed.Round(time.Second).String()
}

func (o *observerReportOverlay) emptyMessage() string {
	switch {
	case !o.observer.Enabled:
		return o.theme.tr("The observer is disabled. Enable it with /observer, then press Ctrl+E again.")
	case !o.observer.HasKey:
		return o.theme.tr("The observer has no API key. Set DUO_OBSERVER_API_KEY, the configured key environment variable, or save a key in observer settings.")
	}
	return o.theme.tr("No report yet. Press R to ask the observer what the agents are doing right now.")
}

func observerNoticeTimestamp(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return " (" + at.Format("15:04") + ")"
}

func observerNoticeText(notice string) string {
	return strings.TrimPrefix(notice, "observer: ")
}

func (o *observerReportOverlay) place(l layout) {
	o.box = centered(l, 92, 30)
	o.Block.SetRect(o.box.Min.X, o.box.Min.Y, o.box.Max.X, o.box.Max.Y)
	if o.width != o.Inner.Dx() {
		o.width = o.Inner.Dx()
		o.reflow()
	}
}

func (o *observerReportOverlay) Draw(buf *ui.Buffer) {
	fillBlock(buf, o.GetRect(), ui.Style{Fg: o.theme.Fg, Bg: o.theme.PanelHigh})
	o.Title = " " + o.theme.tr("OBSERVER REPORTS") + " "
	o.Block.Draw(buf)
	height := max(0, o.Inner.Dy()-1)
	for i := 0; i < height; i++ {
		idx := o.offset + i
		if idx >= len(o.lines) {
			break
		}
		x := o.Inner.Min.X
		for _, run := range o.lines[idx].runs {
			buf.SetString(run.text, run.style, image.Pt(x, o.Inner.Min.Y+i))
			x += rw.StringWidth(run.text)
		}
	}
	hint := o.theme.tr("R re-analyze  ·  ↑/↓ scroll  ·  Esc close")
	if o.Inner.Dy() > 0 {
		buf.SetString(fitText(hint, o.Inner.Dx()), o.theme.Timestamp, image.Pt(o.Inner.Min.X, o.Inner.Max.Y-1))
	}
}

func (o *observerReportOverlay) Scroll(delta int) {
	o.offset = min(max(0, o.offset+delta), o.maxOffset())
}
