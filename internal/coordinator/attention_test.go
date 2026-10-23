package coordinator

import (
	"context"
	"testing"

	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
)

// attentionRecorder drains the bus and returns the attention reasons seen so far.
// It drains on read rather than in a goroutine: an emit is synchronous, so a
// background reader would race the assertion that follows the action.
func attentionRecorder(t *testing.T, bus *events.Bus) func() []string {
	t.Helper()
	ch, cancel := bus.Subscribe(256)
	t.Cleanup(cancel)

	var seen []string
	return func() []string {
		for {
			select {
			case event := <-ch:
				if event.Kind == events.KindAttention {
					seen = append(seen, event.Attention)
					continue
				}
			default:
				return append([]string(nil), seen...)
			}
		}
	}
}

func sawAttention(seen []string, reason string) bool {
	for _, got := range seen {
		if got == reason {
			return true
		}
	}
	return false
}

// TestAttentionVerificationRequestIsAnnounced is the Fast-mode case that hangs
// longest in practice: Austin is done, Tony has not answered, and the human has
// switched terminal tabs.
func TestAttentionVerificationRequestIsAnnounced(t *testing.T) {
	ctx := context.Background()
	runtime := startE2EWithMode(t, ctx, project.ModeFast)
	seen := attentionRecorder(t, runtime.bus)
	austin := runtime.dialAgent(t, protocol.Austin)

	fastRequestVerification(t, austin)

	waitFor(t, func() bool { return sawAttention(seen(), events.AttentionVerification) }, "an attention event for the verification request")
}

// TestAttentionDeliveryPendingIsAnnounced covers the failure that costs the most:
// the work is approved and Duo cannot apply it, so only a human can unblock it.
func TestAttentionDeliveryPendingIsAnnounced(t *testing.T) {
	ctx := context.Background()
	runtime := startE2E(t, ctx)
	seen := attentionRecorder(t, runtime.bus)

	runtime.coord.notifyDeliveryPending(ctx, sessionstore.Delivery{
		Status:      sessionstore.DeliveryBlocked,
		Reason:      "the user's working tree is dirty",
		FinalHead:   "abcdef123456",
		FinalBranch: "duo/session/austin",
	})

	if !sawAttention(seen(), events.AttentionDelivery) {
		t.Fatalf("no delivery attention event; saw %v", seen())
	}
}

// TestAttentionSummaryReportIsAnnounced is the message Duo promised the human,
// which arrives after delivery when nobody is watching the dashboard.
func TestAttentionSummaryReportIsAnnounced(t *testing.T) {
	ctx := context.Background()
	runtime := startE2E(t, ctx)
	seen := attentionRecorder(t, runtime.bus)
	report := protocol.SummaryRuleLine() + "\n| Duo final report |\n" + protocol.SummaryRuleLine() + "\nresult"

	runtime.coord.handleAssistant(protocol.Austin, protocol.Message{Text: report})

	if !sawAttention(seen(), events.AttentionSummary) {
		t.Fatalf("Austin's final report announced nothing; saw %v", seen())
	}

	// An ordinary message, and Tony's report, are not the promised summary.
	before := len(seen())
	runtime.coord.handleAssistant(protocol.Tony, protocol.Message{Text: report})
	runtime.coord.handleAssistant(protocol.Austin, protocol.Message{Text: "just a normal reply"})
	if len(seen()) != before {
		t.Fatalf("a message that is not Austin's final report announced attention: %v", seen())
	}
}

// TestAttentionMergeConflictIsAnnounced is the case where the session cannot move
// at all: the merge conflicts, and it sits there silently.
func TestAttentionMergeConflictIsAnnounced(t *testing.T) {
	ctx := context.Background()
	runtime := startE2E(t, ctx)
	seen := attentionRecorder(t, runtime.bus)

	writeAndCommit(t, runtime.set.Austin.Path, "shared.md", "from Austin\n", "austin line")
	writeAndCommit(t, runtime.set.Tony.Path, "shared.md", "from Tony\n", "tony line")

	if text := runtime.coord.beginIntegration(ctx); text == "" {
		t.Fatal("beginIntegration returned no text for the conflict")
	}
	if !sawAttention(seen(), events.AttentionConflict) {
		t.Fatalf("a merge conflict announced nothing; saw %v", seen())
	}
	// The merge is left in progress, which is the state a human is told about. The
	// test aborts it so the worktree cleanup is not left with a dirty tree.
	e2eRun(t, runtime.set.Austin.Path, "git", "merge", "--abort")
}

// TestAttentionReasonsAreTheDocumentedSet keeps the four reasons a frontend can
// localise from drifting: a new reason with no text is a silent notification.
func TestAttentionReasonsAreTheDocumentedSet(t *testing.T) {
	for _, reason := range []string{
		events.AttentionConflict,
		events.AttentionDelivery,
		events.AttentionVerification,
		events.AttentionSummary,
	} {
		if reason == "" {
			t.Error("an attention reason is empty")
		}
	}
}
