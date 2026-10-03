package coordinator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
)

// awaitMessage decodes what Duo pushed to an agent until one satisfies match.
// A request that belongs to a milestone arrives after that milestone's own
// response, so it can only be observed by reading past it.
func (c *testAgent) awaitMessage(t *testing.T, what string, match func(protocol.Message) bool) protocol.Message {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.conn.SetReadDeadline(time.Time{}) }()
	for {
		var message protocol.Message
		if err := c.dec.Decode(&message); err != nil {
			t.Fatalf("waiting for %s: %v", what, err)
		}
		if match(message) {
			return message
		}
	}
}

// send writes an agent's own free text, which carries no request id and is
// answered with no response.
func (c *testAgent) send(t *testing.T, message protocol.Message) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.enc.Encode(message); err != nil {
		t.Fatal(err)
	}
}

const summaryMarker = "[Duo final summary request]"

// summaryRequest matches the one notice that opens the final report, ignoring
// the DONE phase broadcast that reaches the same agent first.
func summaryRequest(m protocol.Message) bool {
	return m.Type == protocol.MsgDuoNotice && strings.Contains(m.Text, summaryMarker)
}

// TestDoneAsksAustinToSummariseTheSession is the deliverable of this feature: at
// DONE, in either mode, Duo asks Austin to account for the whole session, and
// the answer comes back to the human as `Austin → Human` rather than as agent
// traffic or a Duo notice. Both halves run over the real transport.
func TestDoneAsksAustinToSummariseTheSession(t *testing.T) {
	for _, mode := range []project.Mode{project.ModeFast, project.ModeGoal} {
		t.Run(mode.String(), func(t *testing.T) {
			ctx := context.Background()
			runtime := startE2EWithMode(t, ctx, mode)
			set, state, server := runtime.set, runtime.state, runtime.server

			austin := runtime.dialAgent(t, protocol.Austin)
			tony := runtime.dialAgent(t, protocol.Tony)
			waitFor(t, func() bool { return server.IsConnected(protocol.Austin) && server.IsConnected(protocol.Tony) }, "both agents to connect")
			seen, cancel := runtime.bus.Subscribe(64)
			defer cancel()

			writeAndCommit(t, set.Austin.Path, "result.md", "hello Duo\n", "add result.md")
			if mode == project.ModeGoal {
				request(t, austin, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetPlan, Plan: "Write result.md"})
				writeAndCommit(t, set.Tony.Path, "tony-draft.md", "scratch\n", "tony draft")
			}
			driveToDone(t, mode, runtime, austin, tony)

			// Duo asks Austin, not the peer: the summary is the human's. A
			// request aimed at Tony alone would never arrive on this socket.
			notice := austin.awaitMessage(t, "the final summary request", summaryRequest)
			if notice.From != protocol.Duo || notice.To != protocol.Austin {
				t.Fatalf("summary request is %s → %s, want Duo → Austin", notice.From, notice.To)
			}
			for _, want := range []string{
				"The process", "The results", "Austin → Human",
				"Do not use duo_send", "do not start new work", mode.Display(),
			} {
				if !strings.Contains(notice.Text, want) {
					t.Fatalf("summary request missing %q:\n%s", want, notice.Text)
				}
			}

			// Austin's answer reaches the human: an assistant event with no peer
			// target is what the timeline renders as `Austin → Human`.
			const report = "I added result.md and Duo delivered it."
			austin.send(t, protocol.Message{
				Version: protocol.Version, Type: protocol.MsgAssistantMessage,
				Agent: protocol.Austin, Text: report, Timestamp: time.Now().UnixMilli(),
			})
			waitFor(t, func() bool {
				for {
					select {
					case event := <-seen:
						if event.Kind == events.KindAssistant && event.Agent == protocol.Austin && event.Text == report {
							if event.Peer != "" {
								t.Fatalf("summary reply targets %q, want the human", event.Peer)
							}
							return true
						}
					default:
						return false
					}
				}
			}, "Austin to answer the human")

			if state.Snapshot().Phase != project.PhaseDone {
				t.Fatalf("asking for a summary changed the phase to %s", state.Snapshot().Phase)
			}
		})
	}
}

// driveToDone runs a whole round in either mode up to the delivered DONE
// checkpoint, using the phase chain the mode actually has.
func driveToDone(t *testing.T, mode project.Mode, runtime *e2eRuntime, austin, tony *testAgent) {
	t.Helper()
	state, set := runtime.state, runtime.set
	signBoth := func() {
		sign(t, austin)
		sign(t, tony)
	}
	if mode == project.ModeGoal {
		signBoth()
		waitPhase(t, state, project.PhaseExecute)
		signBoth()
		waitPhase(t, state, project.PhaseReview)
		beforeMerge := gitHead(t, set.Austin.Path)
		signBoth()
		waitPhase(t, state, project.PhaseIntegrate)
		waitFor(t, func() bool { return gitHead(t, set.Austin.Path) != beforeMerge }, "Tony's work to be merged into Austin")
		signBoth()
		waitPhase(t, state, project.PhaseDone)
		return
	}
	fastRequestVerification(t, austin)
	waitPhase(t, state, project.PhaseVerify)
	if _, err := fastVerdict(t, tony, "passed", ""); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, state, project.PhaseDone)
}

// TestFinalSummaryIsRequestedOncePerRound pins the delivery retry: the applied
// checkpoint is written before DONE, so the delivery transaction can be entered
// twice. The human must still receive exactly one report, and the round the
// human opens afterwards must report on its own delivery.
func TestFinalSummaryIsRequestedOncePerRound(t *testing.T) {
	coord := promptCoordinator()
	if !coord.claimFinalSummary() {
		t.Fatal("the first DONE of a round must ask for a summary")
	}
	if coord.claimFinalSummary() {
		t.Fatal("a retried delivery must not ask Austin to report the same round twice")
	}
	coord.unclaimFinalSummary()
	if !coord.claimFinalSummary() {
		t.Fatal("a reopened round must ask for its own summary")
	}
}
