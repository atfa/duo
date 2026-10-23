package gotui

import (
	"strings"
	"testing"

	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/session"
	"github.com/atfa/duo/internal/sessionstore"
)

func costTestLedger() *sessionstore.UsageLedger {
	return &sessionstore.UsageLedger{
		Agents: map[protocol.AgentID]sessionstore.UsageTotals{
			protocol.Austin: {Turns: 12, InputTokens: 9000, OutputTokens: 3000, TotalTokens: 15200, CostUSD: 0.42},
			protocol.Tony:   {Turns: 3, InputTokens: 2000, OutputTokens: 1000, TotalTokens: 4800, CostUSD: 0.13},
		},
		Phases: map[string]sessionstore.UsageTotals{
			"PLAN":   {Turns: 5, TotalTokens: 6000, CostUSD: 0.20},
			"EXECUTE": {Turns: 10, TotalTokens: 14000, CostUSD: 0.35},
		},
	}
}

func costPanelText(t *testing.T, view session.View) string {
	t.Helper()
	c := newCost(newTheme())
	c.width = 76
	c.Block.SetRect(0, 0, 80, 20)
	c.begin(view)
	var sb strings.Builder
	for _, line := range c.lines {
		for _, run := range line.runs {
			sb.WriteString(run.text)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// TestCostPanelRendersPerAgentTotals is the panel's contract: every agent line,
// every phase line, and the session total, all from the ledger the coordinator
// published — the panel counts nothing itself.
func TestCostPanelRendersPerAgentTotals(t *testing.T) {
	text := costPanelText(t, session.View{Usage: costTestLedger()})

	for _, want := range []string{
		"Austin  12 turns · 15.2k tok · $0.42",
		"Tony    3 turns · 4.8k tok · $0.13",
		"PLAN", "EXECUTE",
		"Session cost: $0.55 · 20k tokens · 15 turns",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("cost panel is missing %q; rendered:\n%s", want, text)
		}
	}
}

// A nil ledger is "not measured", not zero: the panel says so and shows no figures.
func TestCostPanelSaysUnmeasuredForNilLedger(t *testing.T) {
	text := costPanelText(t, session.View{})
	if !strings.Contains(text, "no usage measured yet") {
		t.Errorf("nil ledger must render as unmeasured; rendered:\n%s", text)
	}
	if strings.Contains(text, "$0.00") {
		t.Errorf("unmeasured must not render a zero cost; rendered:\n%s", text)
	}
}

// The cost sentence follows /language, in the same words the final summary uses.
func TestCostPanelChineseTotal(t *testing.T) {
	zh := "zh"
	theme := newTheme()
	theme.Lang = &zh
	c := newCost(theme)
	c.width = 76
	c.Block.SetRect(0, 0, 80, 20)
	c.begin(session.View{Usage: costTestLedger()})
	var sb strings.Builder
	for _, line := range c.lines {
		for _, run := range line.runs {
			sb.WriteString(run.text)
		}
		sb.WriteString("\n")
	}
	if !strings.Contains(sb.String(), "会话成本：$0.55 · 20k tokens · 15 轮") {
		t.Errorf("zh cost panel missing the translated total; rendered:\n%s", sb.String())
	}
}
