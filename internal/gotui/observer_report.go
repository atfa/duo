package gotui

import (
	"image"
	"strings"
	"time"

	"github.com/atfa/duo/internal/session"
	rw "github.com/mattn/go-runewidth"
	ui "github.com/metaspartan/gotui/v5"
)

// observerReportOverlay presents the observer's saved-in-session progress
// reports without mixing them up with observer configuration.
type observerReportOverlay struct {
	*ui.Block
	theme    theme
	box      image.Rectangle
	width    int
	offset   int
	lines    []renderLine
	reports  []entry
	observer session.ObserverView
}

func newObserverReports(t theme) *observerReportOverlay {
	return &observerReportOverlay{Block: newModalBlock(t), theme: t, width: -1}
}

func (o *observerReportOverlay) begin(entries []entry, observer session.ObserverView) {
	o.offset = 0
	var reports []entry
	for _, e := range entries {
		if strings.HasPrefix(e.text, "Observer: ") {
			reports = append(reports, e)
		}
	}
	o.reports = reports
	o.observer = observer
	o.reflow()
}

func (o *observerReportOverlay) updateObserver(observer session.ObserverView) {
	if observerSame(o.observer, observer) {
		return
	}
	o.observer = observer
	o.reflow()
}

func (o *observerReportOverlay) hasReports() bool { return len(o.reports) > 0 }

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
	if !o.hasReports() {
		message := "No observer reports yet. Enable the observer and reports will appear here as work progresses."
		switch {
		case !o.observer.Enabled:
			// Keep the disabled state distinct from an enabled observer that has
			// not produced a report yet.
		case !o.observer.HasKey:
			message = "The observer has no API key. Set DUO_OBSERVER_API_KEY, the configured key environment variable, or save a key in observer settings."
		case o.observer.LastNotice != "":
			message = o.theme.tr("Latest observer notice") + observerNoticeTimestamp(o.observer.LastNoticeAt) + ": " + observerNoticeText(o.observer.LastNotice)
		default:
			message = "The observer is enabled; no new report is available yet. Reports appear as work progresses."
		}
		if o.observer.Enabled && o.observer.HasKey && o.observer.LastNotice != "" {
			o.lines = wrapText(message, o.theme.BodyMuted, width)
		} else {
			o.lines = wrapText(o.theme.tr(message), o.theme.BodyMuted, width)
		}
		return
	}
	if o.observer.LastNotice != "" {
		message := o.theme.tr("Latest observer notice") + observerNoticeTimestamp(o.observer.LastNoticeAt) + ": " + observerNoticeText(o.observer.LastNotice)
		o.lines = append(o.lines, wrapText(message, o.theme.BodyMuted, width)...)
		o.lines = append(o.lines, makeLine("", o.theme.BodyMuted, 0))
	}
	for i := len(o.reports) - 1; i >= 0; i-- {
		e := o.reports[i]
		stamp := e.at.Format("15:04")
		o.lines = append(o.lines, makeLine(stamp, ui.Style{Fg: o.theme.Accent, Bg: o.theme.Panel, Modifier: ui.ModifierBold}, 0))
		o.lines = append(o.lines, wrapText(strings.TrimPrefix(e.text, "Observer: "), o.theme.BodyMuted, width)...)
		o.lines = append(o.lines, makeLine("", o.theme.BodyMuted, 0))
	}
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
	hint := o.theme.tr("↑/↓ scroll  ·  Esc close")
	if o.Inner.Dy() > 0 {
		buf.SetString(fitText(hint, o.Inner.Dx()), o.theme.Timestamp, image.Pt(o.Inner.Min.X, o.Inner.Max.Y-1))
	}
}

func (o *observerReportOverlay) Scroll(delta int) {
	o.offset = min(max(0, o.offset+delta), max(0, len(o.lines)-max(1, o.Inner.Dy()-1)))
}
