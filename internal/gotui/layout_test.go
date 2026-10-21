package gotui

import "testing"

func TestSidebarPanelExpansion(t *testing.T) {
	app := New(nil)
	normal := app.computeLayout(120, 42)
	if !normal.sidebarVisible {
		t.Fatal("sidebar should be visible at 120x42")
	}
	if normal.workflow.Dy() != 12 || normal.austinCard.Dy() != 10 || normal.tonyCard.Dy() != 10 {
		t.Fatalf("unexpected normal panel heights: workflow=%d austin=%d tony=%d",
			normal.workflow.Dy(), normal.austinCard.Dy(), normal.tonyCard.Dy())
	}
	if normal.main.Min.Y != normal.header.Max.Y {
		t.Fatalf("view tabs still occupy their own row: main starts at %d, header ends at %d",
			normal.main.Min.Y, normal.header.Max.Y)
	}
	if normal.tabSplit.Min.Y != normal.header.Max.Y-1 || normal.tabTimeline.Min.Y != normal.tabSplit.Min.Y {
		t.Fatal("view tabs are not on the header divider row")
	}

	cases := []struct {
		focus    sidebarPanel
		workflow int
		austin   int
		tony     int
	}{
		{panelWorkflow, 25, 3, 3},
		{panelAustin, 3, 25, 3},
		{panelTony, 3, 3, 25},
	}
	for _, tc := range cases {
		app.sidebarFocus = tc.focus
		l := app.computeLayout(120, 42)
		if l.workflow.Dy() != tc.workflow || l.austinCard.Dy() != tc.austin || l.tonyCard.Dy() != tc.tony {
			t.Fatalf("focus %d: heights workflow=%d austin=%d tony=%d, want %d/%d/%d",
				tc.focus, l.workflow.Dy(), l.austinCard.Dy(), l.tonyCard.Dy(),
				tc.workflow, tc.austin, tc.tony)
		}
		if l.workflow.Min.Y != l.sidebar.Min.Y {
			t.Fatalf("focus %d: workflow does not start at the sidebar top", tc.focus)
		}
		if l.austinCard.Min.Y != l.workflow.Max.Y+1 || l.tonyCard.Min.Y != l.austinCard.Max.Y+1 {
			t.Fatalf("focus %d: panels overlap or lose their one-row gap", tc.focus)
		}
		if l.tonyCard.Max.Y != l.sidebar.Max.Y {
			t.Fatalf("focus %d: panels do not fill the sidebar: tony max=%d sidebar max=%d",
				tc.focus, l.tonyCard.Max.Y, l.sidebar.Max.Y)
		}
	}
}
