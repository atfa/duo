package coordinator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
)

// The final report used to be recognised by its drawn title box, a rule the
// model reproduced unreliably and the interface read only off the first
// non-empty line — so a cost line above the box disabled the marking entirely.
// v5 replaces that with coordinator state: Duo knows it asked, and marks the
// next assistant message from Austin that arrives. These tests pin that
// mechanism from both sides.

// TestFinalSummaryMarkedByRequestNotDecoration proves the marking follows the
// request, not the text. Half (a): a plain unadorned report after the request
// is marked and fires the summary attention. Half (b): a box-shaped message
// with no outstanding request is not marked — the old content-based detection
// marked it, so this test fails on the pre-v5 implementation.
func TestFinalSummaryMarkedByRequestNotDecoration(t *testing.T) {
	ctx := context.Background()
	runtime := startE2EWithMode(t, ctx, project.ModeFast)
	set, server := runtime.set, runtime.server

	austin := runtime.dialAgent(t, protocol.Austin)
	tony := runtime.dialAgent(t, protocol.Tony)
	waitFor(t, func() bool { return server.IsConnected(protocol.Austin) && server.IsConnected(protocol.Tony) }, "both agents to connect")
	seen, cancel := runtime.bus.Subscribe(64)
	defer cancel()

	writeAndCommit(t, set.Austin.Path, "result.md", "hello Duo\n", "add result.md")
	driveToDone(t, project.ModeFast, runtime, austin, tony)

	// The DONE transition may already have armed the real request; consume it
	// so the box-shaped message below is genuinely "no outstanding request".
	runtime.coord.takeSummaryOwed()

	// Half (b): a box-shaped message while nothing is owed must arrive as
	// ordinary agent traffic. The old detection read the shape and marked it,
	// so a regression that adds it back turns sawBox true and this test red.
	const boxLike = "============================================\n| IMITATION |\n============================================"
	austin.send(t, protocol.Message{
		Version: protocol.Version, Type: protocol.MsgAssistantMessage,
		Agent: protocol.Austin, Text: boxLike, Timestamp: time.Now().UnixMilli(),
	})
	var sawBox bool
	waitFor(t, func() bool {
		for {
			select {
			case event := <-seen:
				if event.Kind == events.KindAssistant && event.Agent == protocol.Austin && event.Text == boxLike {
					sawBox = event.Summary
					return true
				}
			default:
				return false
			}
		}
	}, "the box-shaped message to reach the bus")
	if sawBox {
		t.Fatalf("a box-shaped message with no outstanding request was marked")
	}

	// Half (a): the request goes out; the plain report that answers it is the
	// one the human gets marked and notified about.
	runtime.coord.oweFinalSummary()
	const report = "Result: result.md added. Delivered."
	austin.send(t, protocol.Message{
		Version: protocol.Version, Type: protocol.MsgAssistantMessage,
		Agent: protocol.Austin, Text: report, Timestamp: time.Now().UnixMilli(),
	})

	var sawReport, sawAttention bool
	waitFor(t, func() bool {
		for {
			select {
			case event := <-seen:
				if event.Kind == events.KindAssistant && event.Agent == protocol.Austin && event.Text == report {
					sawReport = event.Summary
				}
				if event.Kind == events.KindAttention && event.Attention == events.AttentionSummary {
					sawAttention = true
				}
			default:
				return sawReport
			}
		}
	}, "the plain report to be marked")
	if !sawAttention {
		t.Fatalf("the marked report did not fire the summary attention")
	}
}

// TestFinalSummaryPromptForbidsDecorations checks both language versions of
// the prompt: no title-box instruction survives (neither the old rule width nor
// a drawn frame), and the explicit ban on banners is present so the model does
// not invent its own decoration. The ban sentence itself names boxes, so the
// check is for the *requesting* phrases, not the word "box".
func TestFinalSummaryPromptForbidsDecorations(t *testing.T) {
	banned := []string{"67", "SummaryRuleLine", "==========="}
	bannedZh := []string{"上下两行", "首尾各一个", "67 个"}
	phrases := map[string][]string{
		"en": {"Do not draw a banner, rule or title box"},
		"zh": {"不要画横幅、分隔线或标题框"},
	}
	for _, language := range []string{"en", "zh"} {
		coord := promptCoordinator()
		coord.language = language
		got := coord.finalSummaryPrompt(project.Snapshot{PlanVersion: 1})
		for _, bad := range append(banned, bannedZh...) {
			if strings.Contains(got, bad) {
				t.Fatalf("%s prompt still asks for the title box (%q):\n%s", language, bad, got)
			}
		}
		for _, want := range phrases[language] {
			if !strings.Contains(got, want) {
				t.Fatalf("%s prompt missing the no-decoration instruction %q:\n%s", language, want, got)
			}
		}
	}
}

// TestTUIEntrySummaryRoundTrip pins the persistence contract: the mark survives
// a save/load cycle, and a log written before the field existed decodes as
// unmarked rather than failing.
func TestTUIEntrySummaryRoundTrip(t *testing.T) {
	store, err := sessionstore.New(t.TempDir(), "repo", "session")
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	log := store.OpenEvents()
	at := time.Now()
	marked := sessionstore.TUIEntry{Time: at, Pane: "Austin", Text: "the report", Label: "Austin → Human", Summary: true}
	log.RecordTUIEntry(marked)
	log.RecordTUIEntry(sessionstore.TUIEntry{Time: at, Pane: "Austin", Text: "plain traffic"})

	entries := store.OpenEvents().TUIEntries()
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if !entries[0].Summary {
		t.Fatalf("the marked report lost its mark on round trip: %+v", entries[0])
	}
	if entries[1].Summary {
		t.Fatalf("plain traffic came back marked: %+v", entries[1])
	}
}
