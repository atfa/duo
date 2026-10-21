package gotui

import (
	"testing"

	"github.com/atfa/duo/internal/models"
	"github.com/atfa/duo/internal/protocol"
)

func TestPickerThinkingLevelLabel(t *testing.T) {
	p := newModelPicker(newTheme())
	for level, want := range map[string]string{
		"off": "off", "minimal": "minimal", "low": "low", "medium": "medium",
		"high": "high", "xhigh": "xhigh", "max": "max", "": "default",
	} {
		p.thinking = level
		if got := p.thinkingLevelLabel(); got != want {
			t.Errorf("thinking %q label = %q, want %q", level, got, want)
		}
	}
}

func TestPickerSelectsCurrentModelAfterAsyncLoad(t *testing.T) {
	p := newModelPicker(newTheme())
	if !p.begin(protocol.Austin, "anthropic/claude-3", "high") {
		t.Fatal("begin did not request an initial catalog load")
	}
	p.beginLoad()
	p.loaded(protocol.Austin, []models.Model{
		{Provider: "openai", ID: "gpt-5"},
		{Provider: "anthropic", ID: "claude-3"},
	}, nil)
	selected, ok := p.selected()
	if !ok {
		t.Fatal("no model selected")
	}
	if selected.Reference() != "anthropic/claude-3" {
		t.Fatalf("selected %q, want the currently active model", selected.Reference())
	}
}
