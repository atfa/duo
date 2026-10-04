package tui

import (
	"strings"
	"testing"

	"github.com/atfa/duo/internal/project"
)

// TestFrameModeBadgeAndBorderColour is the independent check for the top-row
// mode badge and the mode-tinted border. It deliberately uses literal escape
// codes and pre-existing APIs rather than the implementation's new constants or
// helpers, so it tests user-visible behaviour and stays valid if the helpers are
// refactored.
func TestFrameModeBadgeAndBorderColour(t *testing.T) {
	cases := []struct {
		name       string
		mode       project.Mode
		badge      string
		titleCode  string
		borderCode string
	}{
		{"fast", project.ModeFast, "[FAST]", "\x1b[1;36m", "\x1b[36m"},
		{"goal", project.ModeGoal, "[GOAL]", "\x1b[1;35m", "\x1b[35m"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := testApp(100, 30)
			a.setProject(project.NewStateFor(tc.mode))

			frame := a.buildFrame(renderNormal)
			if !strings.Contains(frame, tc.titleCode+" "+tc.badge) {
				t.Fatalf("frame is missing the bold %s badge (%q)", tc.badge, tc.titleCode+" "+tc.badge)
			}
			if !strings.Contains(frame, tc.borderCode) {
				t.Fatalf("frame border is not tinted with %q", tc.borderCode)
			}

			top := visibleLines(frame)[0]
			if !strings.HasPrefix(top, "┌ "+tc.badge+" ") {
				t.Fatalf("top row = %q, want a leading %s badge", top, tc.badge)
			}
			if got := displayWidth(top); got != 100 {
				t.Fatalf("top row is %d columns, want 100", got)
			}
			// The badge must be absorbed by the title fit, never shifting the ┬
			// that aligns the top row with the pane divider below it.
			split := a.layout().leftW + 1
			topRunes := []rune(top)
			if split >= len(topRunes) || topRunes[split] != '┬' {
				t.Fatalf("top-row divider column %d is not ┬: %q", split, top)
			}
		})
	}
}

// TestFrameBorderColourDiffersByMode proves the two modes do not share a border
// colour, so the mode is visible at a glance.
func TestFrameBorderColourDiffersByMode(t *testing.T) {
	fast := testApp(100, 30)
	fast.setProject(project.NewStateFor(project.ModeFast))
	goal := testApp(100, 30)
	goal.setProject(project.NewStateFor(project.ModeGoal))

	fastFrame := fast.buildFrame(renderNormal)
	goalFrame := goal.buildFrame(renderNormal)

	if !strings.Contains(fastFrame, "\x1b[36m") || !strings.Contains(goalFrame, "\x1b[35m") {
		t.Fatal("frames do not carry the expected cyan/magenta borders")
	}
	if strings.Contains(fastFrame, "\x1b[35m") {
		t.Fatal("FAST frame unexpectedly uses the GOAL border colour")
	}
}
