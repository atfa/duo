package gotui

import (
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/session"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/atfa/duo/internal/workspace"
	rw "github.com/mattn/go-runewidth"
	ui "github.com/metaspartan/gotui/v5"
)

// labelValueColumns reports the display column where each label row's value
// starts. A row counts only when the label opens the row's content, so a value
// that happens to contain a label word is not mistaken for a label row.
func labelValueColumns(t *testing.T, text string, labels []string) map[string]int {
	t.Helper()
	columns := map[string]int{}
	for _, row := range strings.Split(text, "\n") {
		for _, label := range labels {
			if _, seen := columns[label]; seen {
				continue
			}
			at := strings.Index(row, label)
			if at < 0 || strings.Trim(row[:at], "│ ") != "" {
				continue
			}
			rest := row[at+len(label):]
			value := strings.TrimLeft(rest, " ")
			if value == rest {
				continue // the label is the whole line
			}
			columns[label] = rw.StringWidth(row[:len(row)-len(value)])
		}
	}
	return columns
}

// requireOneColumn fails unless every label row puts its value on the same
// column. Labels are measured, not counted: the same row is one cell narrower
// in zh because the translated label is not the same display width.
func requireOneColumn(t *testing.T, widget, lang string, columns map[string]int, labels []string) {
	t.Helper()
	want := -1
	for _, label := range labels {
		column, ok := columns[label]
		if !ok {
			t.Fatalf("%s/%s: no row rendered for %q (got %v)", widget, lang, label, columns)
		}
		if want < 0 {
			want = column
		}
		if column != want {
			t.Fatalf("%s/%s: %q value at column %d, want %d (all %v)", widget, lang, label, column, want, columns)
		}
	}
}

func TestAgentCardLabelsShareOneColumn(t *testing.T) {
	for _, lang := range []string{"en", "zh"} {
		th := newTheme()
		*th.Lang = lang
		fake := newFakeSession()
		av := fake.view.Agents[protocol.Austin]
		started := time.Date(2026, 10, 20, 16, 0, 0, 0, time.Local)
		av.Runtime = harness.AgentRuntime{
			Busy: true, ProviderActive: true, ToolDepth: 1, Tools: 3,
			Tool: "exec", ToolDetail: "go test ./...", ContextTokens: 48210, ContextWindow: 200000,
			TurnStarted: started, LastError: "boom",
		}
		card := newAgentCard(th, protocol.Austin)
		card.av = av
		card.now = started.Add(95 * time.Second)
		card.SetRect(0, 0, 40, 14)
		buf := ui.NewBuffer(card.GetRect())
		card.Draw(buf)
		labels := []string{th.tr("model"), th.tr("driver"), th.tr("thinking"), th.tr("context"), th.tr("tool"), th.tr("turn"), th.tr("error")}
		columns := labelValueColumns(t, bufferText(buf), labels)
		requireOneColumn(t, "agentCard", lang, columns, labels)
	}
}

func TestWorkflowLabelsShareOneColumn(t *testing.T) {
	for _, lang := range []string{"en", "zh"} {
		th := newTheme()
		*th.Lang = lang
		fast := session.View{
			Project: project.Snapshot{
				Mode:         project.ModeFast,
				Phase:        project.PhaseVerify,
				Started:      true,
				Verification: project.Verification{Status: project.VerificationPassed},
			},
			Delivery:  sessionstore.Delivery{Status: sessionstore.DeliveryPending},
			Worktrees: workspace.Set{Repository: "/tmp/example", BaseBranch: "main", Session: "s"},
		}
		step := session.View{
			Project: project.Snapshot{
				Mode:        project.ModeStep,
				Phase:       project.PhaseExecute,
				Started:     true,
				PlanVersion: 3,
				Plan:        "write the plan",
				Steps:       []project.Step{{Title: "add ModeStep"}, {Title: "redesign the scheduler"}},
			},
			Delivery:  sessionstore.Delivery{Status: sessionstore.DeliveryPending},
			Worktrees: workspace.Set{Repository: "/tmp/example", BaseBranch: "main", Session: "s"},
		}
		for _, tc := range []struct {
			name   string
			view   session.View
			labels []string
		}{
			{"fast", fast, []string{th.tr("verify"), th.tr("delivery"), th.tr("worktrees")}},
			{"step", step, []string{th.tr("plan"), th.tr("steps"), th.tr("current"), th.tr("delivery"), th.tr("worktrees")}},
		} {
			panel := newWorkflow(th)
			panel.view = tc.view
			panel.SetRect(0, 0, 36, 14)
			buf := ui.NewBuffer(panel.GetRect())
			panel.Draw(buf)
			columns := labelValueColumns(t, bufferText(buf), tc.labels)
			requireOneColumn(t, "workflow/"+tc.name, lang, columns, tc.labels)
		}
	}
}
