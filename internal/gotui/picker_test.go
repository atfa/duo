package gotui

import (
	"testing"

	"github.com/atfa/duo/internal/models"
	"github.com/atfa/duo/internal/protocol"
)

func TestPickerThinkingLevelLabel(t *testing.T) {
	p := newModelPicker(newTheme())
	*p.theme.Lang = "zh"
	for level, want := range map[string]string{
		"off": "思考·关", "low": "思考·低", "medium": "思考·中",
		"high": "思考·强", "xhigh": "思考·极强", "": "思考·默认",
	} {
		p.thinking = level
		if got := p.thinkingLevelLabel(); got != want {
			t.Errorf("thinking %q label = %q, want %q", level, got, want)
		}
	}
	*p.theme.Lang = "en"
	p.thinking = "high"
	if got := p.thinkingLevelLabel(); got != "thinking · high" {
		t.Errorf("English high label = %q", got)
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
