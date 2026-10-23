package coordinator

import (
	"strings"
	"testing"

	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
)

// TestFinalSummaryCarriesTheCostLedger: a finished session should be able to answer
// what it cost, and the figure has to be the ledger's, not a number Austin invents.
// The line is the same shape the interface draws, so the report and the screen agree.
func TestFinalSummaryCarriesTheCostLedger(t *testing.T) {
	tracker := harness.NewTracker()
	coord := promptCoordinator()
	coord.tracker = tracker
	tracker.AddUsage(protocol.Austin, "EXECUTE", protocol.Usage{InputTokens: 4000, OutputTokens: 1000, CostUSD: 0.2})
	tracker.AddUsage(protocol.Tony, "REVIEW", protocol.Usage{InputTokens: 1000, OutputTokens: 250, CostUSD: 0.05})

	snap := project.NewStateFor(project.ModeGoal).Snapshot()
	prompt := coord.finalSummaryPrompt(snap)
	if !strings.Contains(prompt, "Session cost: $0.25 · 6.2k tokens · 2 turns") {
		t.Fatalf("the summary prompt is missing the cost line:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Reproduce this line at the top of your report") {
		t.Fatal("Austin must be told to reproduce the line, not paraphrase it")
	}

	zh := promptCoordinator()
	zh.tracker = tracker
	zh.language = "zh"
	if got := zh.finalSummaryPrompt(snap); !strings.Contains(got, "会话成本：$0.25 · 6.2k tokens · 2 轮") {
		t.Fatalf("the zh summary prompt is missing the cost line:\n%s", got)
	}
}

// TestFinalSummarySaysNothingAboutCostWhenNothingWasMeasured: an unmeasured session
// must not report a cost of zero, which is a claim about a session that spent
// nothing rather than one nobody priced.
func TestFinalSummarySaysNothingAboutCostWhenNothingWasMeasured(t *testing.T) {
	coord := promptCoordinator()

	prompt := coord.finalSummaryPrompt(project.NewStateFor(project.ModeGoal).Snapshot())
	if strings.Contains(prompt, "Session cost") {
		t.Fatalf("an unmeasured session must not claim a cost:\n%s", prompt)
	}
	if coord.costLine() != "" {
		t.Fatal("costLine must be empty when nothing was measured")
	}
}
