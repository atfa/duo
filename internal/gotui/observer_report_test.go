package gotui

import (
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/protocol"
	"github.com/gdamore/tcell/v3"
)

func TestCtrlEOpensObserverReportInsteadOfSettings(t *testing.T) {
	app := New(newFakeSession())
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

func TestReportsSlashCommandOpensReportPanel(t *testing.T) {
	app := New(newFakeSession())
	if !app.runSlash("/reports") {
		t.Fatal("/reports was not recognized")
	}
	if app.overlay != overlayObserverReports {
		t.Fatalf("/reports overlay = %v, want observer reports", app.overlay)
	}
}
