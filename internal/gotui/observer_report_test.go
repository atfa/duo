package gotui

import (
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/session"
	"github.com/gdamore/tcell/v3"
)

// panelText renders a report overlay's lines as one string.
func panelText(overlay *observerReportOverlay) string {
	var text strings.Builder
	for _, line := range overlay.lines {
		text.WriteString(line.text())
	}
	return text.String()
}

// normalizeText drops spacing so a test can match wrapped panel text.
func normalizeText(text string) string {
	return strings.NewReplacer(" ", "", "\n", "", "·", "").Replace(text)
}

func reportView(text string) session.ObserverView {
	return session.ObserverView{
		Enabled: true, Model: "exec", HasKey: true, Configured: true,
		Reports: []session.ObserverReport{{
			At:   time.Date(2026, 10, 22, 13, 5, 0, 0, time.Local),
			Text: text,
		}},
	}
}

// Ctrl+E is the human asking "what are they doing now", so it opens the panel and
// asks the observer for a fresh report.
func TestCtrlEOpensReportsAndAsksForAFreshOne(t *testing.T) {
	fake := newFakeSession()
	fake.view.Observer = reportView("**Doing now**\n- Austin is running go test ./...")
	app := New(fake)

	app.handleKey(tcell.NewEventKey(tcell.KeyCtrlE, "", tcell.ModNone))

	if app.overlay != overlayObserverReports {
		t.Fatalf("Ctrl+E overlay = %v, want observer reports", app.overlay)
	}
	if got := fake.snapshotReportRequests(); got != 1 {
		t.Fatalf("Ctrl+E asked for %d reports, want 1", got)
	}
	if !app.observerReports.hasReports() {
		t.Fatal("the panel did not show the report already in the view")
	}
	if got := normalizeText(panelText(app.observerReports)); !strings.Contains(got, "Austinisrunninggotest") {
		t.Fatalf("report panel content = %q", got)
	}
}

func TestCtrlEOpensObserverSettingsWhenModelIsMissing(t *testing.T) {
	app := New(newFakeSession())
	app.handleKey(tcell.NewEventKey(tcell.KeyCtrlE, "", tcell.ModNone))
	if app.overlay != overlayObserver {
		t.Fatalf("Ctrl+E without observer model opened overlay %v, want settings", app.overlay)
	}
}

func TestReportsSlashCommandAsksForAFreshReport(t *testing.T) {
	fake := newFakeSession()
	fake.view.Observer.Model = "exec"
	fake.view.Observer.Enabled = true
	fake.view.Observer.HasKey = true
	app := New(fake)
	if !app.runSlash("/reports") {
		t.Fatal("/reports was not recognized")
	}
	if app.overlay != overlayObserverReports {
		t.Fatalf("/reports overlay = %v, want observer reports", app.overlay)
	}
	if got := fake.snapshotReportRequests(); got != 1 {
		t.Fatalf("/reports asked for %d reports, want 1", got)
	}
}

func TestRReanalyzesInsideTheReportPanel(t *testing.T) {
	fake := newFakeSession()
	fake.view.Observer = reportView("report")
	app := New(fake)
	app.handleKey(tcell.NewEventKey(tcell.KeyCtrlE, "", tcell.ModNone))
	app.handleKey(tcell.NewEventKey(tcell.KeyRune, "r", tcell.ModNone))
	if got := fake.snapshotReportRequests(); got != 2 {
		t.Fatalf("R produced %d report requests, want 2 (open plus re-analyze)", got)
	}
	if app.overlay != overlayObserverReports {
		t.Fatalf("R closed the panel: overlay = %v", app.overlay)
	}
}

// A disabled observer or a missing key must explain itself instead of asking for
// a report nobody can produce.
func TestObserverReportEmptyStateExplainsWhy(t *testing.T) {
	tests := []struct {
		name     string
		observer session.ObserverView
		want     string
	}{
		{name: "disabled", want: "Enable it with /observer"},
		{name: "enabled without key", observer: session.ObserverView{Enabled: true}, want: "no API key"},
		{name: "enabled with a key and no report", observer: session.ObserverView{Enabled: true, HasKey: true}, want: "Press R"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			overlay := newObserverReports(newTheme())
			overlay.begin(tt.observer)
			got := normalizeText(panelText(overlay))
			if !strings.Contains(got, normalizeText(tt.want)) {
				t.Fatalf("empty report panel = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestObserverReportPanelShowsDiagnosticAlongsideExistingReport(t *testing.T) {
	overlay := newObserverReports(newTheme())
	view := reportView("earlier report")
	view.LastNotice = "latest request failed"
	view.LastNoticeAt = time.Date(2026, 10, 22, 13, 6, 0, 0, time.Local)
	overlay.begin(view)
	got := normalizeText(panelText(overlay))
	if !strings.Contains(got, "latestrequestfailed") || !strings.Contains(got, "earlierreport") {
		t.Fatalf("report panel = %q, want both latest diagnostic and report", got)
	}
}

// A request can take as long as the model timeout, so the panel has to show that
// something is happening, and keep counting.
func TestObserverReportShowsAnalyzingWithElapsedTime(t *testing.T) {
	overlay := newObserverReports(newTheme())
	view := session.ObserverView{
		Enabled: true, HasKey: true, Analyzing: true,
		RequestedAt: time.Now().Add(-3 * time.Second),
	}
	overlay.begin(view)
	got := normalizeText(panelText(overlay))
	if !strings.Contains(got, "Askingtheobserver") {
		t.Fatalf("analyzing panel = %q", got)
	}
	if !strings.Contains(got, "3s") {
		t.Fatalf("analyzing panel lost the elapsed time: %q", got)
	}

	// The elapsed row must keep redrawing even though the view value is equal.
	overlay.updateObserver(view)
	if !strings.Contains(normalizeText(panelText(overlay)), "Askingtheobserver") {
		t.Fatal("an unchanged analyzing view stopped redrawing")
	}
}

// The human asked for observer output in the Ctrl+E panel only.
func TestObserverReportNeverBecomesATimelineEntry(t *testing.T) {
	fake := newFakeSession()
	fake.view.Observer = reportView("**Doing now**\n- Tony is running bun test")
	app := New(fake)
	before := len(app.entries)

	app.handleKey(tcell.NewEventKey(tcell.KeyCtrlE, "", tcell.ModNone))

	if len(app.entries) != before {
		t.Fatalf("the report panel added %d Timeline entries: %+v", len(app.entries)-before, app.entries[before:])
	}
	if recorded := fake.snapshotRecorded(); len(recorded) != 0 {
		t.Fatalf("the report was journaled as session history: %+v", recorded)
	}
}

// A re-analysis can return a shorter report set than the one already on screen.
// The viewport has to follow the new content; otherwise an old scroll position
// stays past the end and the panel draws an empty body.
func TestObserverReportKeepsTheScrollInsideTheNewContent(t *testing.T) {
	overlay := newObserverReports(newTheme())
	overlay.Block.SetRect(0, 0, 60, 12)

	long := strings.Repeat("the agents are running the full test suite\n\n", 12)
	overlay.begin(reportView(long))
	overlay.Scroll(1000)
	if overlay.offset == 0 {
		t.Fatal("the test could not scroll the panel")
	}

	overlay.updateObserver(reportView("short report"))
	if overlay.offset != 0 {
		t.Fatalf("offset after a shorter report set = %d, want 0", overlay.offset)
	}
	if got := normalizeText(panelText(overlay)); !strings.Contains(got, "shortreport") {
		t.Fatalf("panel after the clamp = %q, want the new report", got)
	}
}
