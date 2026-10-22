package gotui

import (
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/session"
	"github.com/gdamore/tcell/v3"
)

func TestCtrlEOpensObserverReportInsteadOfSettings(t *testing.T) {
	fake := newFakeSession()
	fake.view.Observer.Model = "exec"
	fake.view.Observer.Enabled = true
	fake.view.Observer.HasKey = true
	app := New(fake)
	app.route(events.Event{
		Time: time.Date(2026, 10, 22, 13, 5, 0, 0, time.Local), Kind: events.KindObserver,
		Agent: protocol.Duo, Text: "Austin completed the parser; tests are still running.",
	})
	app.handleKey(tcell.NewEventKey(tcell.KeyCtrlE, "", tcell.ModNone))
	if app.overlay != overlayObserverReports {
		t.Fatalf("Ctrl+E overlay = %v, want observer reports", app.overlay)
	}
	if !app.observerReports.hasReports() {
		t.Fatal("observer report panel did not include the existing report")
	}
	if app.observer != nil && app.overlay == overlayObserver {
		t.Fatal("Ctrl+E opened observer settings instead of reports")
	}
	var report strings.Builder
	for _, line := range app.observerReports.lines {
		report.WriteString(line.text())
	}
	if !strings.Contains(strings.ReplaceAll(report.String(), " ", ""), "Austincompletedtheparser") {
		t.Fatalf("report panel content = %q", report.String())
	}
}

func TestCtrlEOpensObserverSettingsWhenModelIsMissing(t *testing.T) {
	app := New(newFakeSession())
	app.handleKey(tcell.NewEventKey(tcell.KeyCtrlE, "", tcell.ModNone))
	if app.overlay != overlayObserver {
		t.Fatalf("Ctrl+E without observer model opened overlay %v, want settings", app.overlay)
	}
}

func TestReportsSlashCommandOpensReportPanel(t *testing.T) {
	fake := newFakeSession()
	fake.view.Observer.Model = "exec"
	app := New(fake)
	if !app.runSlash("/reports") {
		t.Fatal("/reports was not recognized")
	}
	if app.overlay != overlayObserverReports {
		t.Fatalf("/reports overlay = %v, want observer reports", app.overlay)
	}
}

func TestObserverReportEmptyStateExplainsWhyReportsAreMissing(t *testing.T) {
	tests := []struct {
		name     string
		observer session.ObserverView
		want     string
	}{
		{
			name: "disabled keeps existing guidance",
			want: "Enable the observer and reports will appear",
		},
		{
			name:     "enabled without key",
			observer: session.ObserverView{Enabled: true},
			want:     "no API key",
		},
		{
			name:     "enabled with no recent report",
			observer: session.ObserverView{Enabled: true, HasKey: true},
			want:     "no new report is available yet",
		},
		{
			name:     "enabled with diagnostic",
			observer: session.ObserverView{Enabled: true, HasKey: true, LastNotice: "model rejected"},
			want:     "model rejected",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			overlay := newObserverReports(newTheme())
			overlay.begin(nil, tt.observer)
			var text strings.Builder
			for _, line := range overlay.lines {
				text.WriteString(line.text())
			}
			normalized := strings.ReplaceAll(strings.ToLower(text.String()), " ", "")
			if !strings.Contains(normalized, strings.ReplaceAll(strings.ToLower(tt.want), " ", "")) {
				t.Fatalf("empty report panel = %q, want %q", text.String(), tt.want)
			}
		})
	}
}

func TestObserverReportPanelShowsDiagnosticAlongsideExistingReports(t *testing.T) {
	overlay := newObserverReports(newTheme())
	overlay.begin([]entry{{text: "Observer: earlier report"}}, session.ObserverView{
		Enabled: true, HasKey: true, LastNotice: "latest request failed",
	})
	var text strings.Builder
	for _, line := range overlay.lines {
		text.WriteString(line.text())
	}
	normalized := strings.ReplaceAll(text.String(), " ", "")
	if !strings.Contains(normalized, "latestrequestfailed") || !strings.Contains(normalized, "earlierreport") {
		t.Fatalf("report panel = %q, want both latest diagnostic and report", text.String())
	}
}
