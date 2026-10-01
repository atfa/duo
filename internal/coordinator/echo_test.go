package coordinator

import (
	"context"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
)

// A prompt Duo injected comes back through the bridge as an assistant_message
// when a driver mis-attributes the user message. Reporting it fills the
// timeline with the human's own words presented as the agent speaking.
func TestInjectedPromptIsNotReportedAsAssistantMessage(t *testing.T) {
	bus := events.NewBus()
	ch, unsub := bus.Subscribe(16)
	defer unsub()

	coord := New(nil, project.NewStateFor(project.ModeFast), harness.NewTracker(), nil, bus)
	task := "[Human task from Duo]\n\nfix the bug"
	coord.noteInjected(protocol.Austin, task)

	coord.handleAssistant(protocol.Austin, protocol.Message{Type: protocol.MsgAssistantMessage, Text: task})
	for {
		select {
		case e := <-ch:
			if e.Text != "" {
				t.Fatalf("echo was reported to the timeline: %q", e.Text)
			}
			continue
		default:
		}
		break
	}

	// Anything the agent actually says must still get through.
	coord.handleAssistant(protocol.Austin, protocol.Message{
		Type: protocol.MsgAssistantMessage,
		Text: "I fixed the parser and added a test.",
	})
	select {
	case e := <-ch:
		if !strings.Contains(e.Text, "I fixed the parser") {
			t.Fatalf("real assistant message was dropped: %q", e.Text)
		}
	default:
		t.Fatal("real assistant message never reached the bus")
	}
}

// The guard must not leak between agents.
func TestEchoGuardIsPerAgent(t *testing.T) {
	coord := New(nil, project.NewStateFor(project.ModeFast), harness.NewTracker(), nil, events.NewBus())
	coord.noteInjected(protocol.Austin, "the same text")
	if coord.wasInjected(protocol.Tony, "the same text") {
		t.Fatal("Tony must not be treated as echoing Austin's prompt")
	}
}

// Sending to an agent records the injection, so the choke point covers every
// prompt type without each call site having to remember.
func TestSendToAgentRecordsInjection(t *testing.T) {
	coord := New(nil, project.NewStateFor(project.ModeFast), harness.NewTracker(), nil, events.NewBus())
	ctx := context.Background()

	_ = coord.sendToAgent(ctx, protocol.Austin, protocol.Message{
		Type: protocol.MsgHumanPrompt, Text: "do the thing",
	})
	if !coord.wasInjected(protocol.Austin, "do the thing") {
		t.Fatal("human_prompt was not recorded as injected")
	}

	_ = coord.sendToAgent(ctx, protocol.Austin, protocol.Message{
		Type: protocol.MsgHarnessPrompt, Text: "wake up",
	})
	if !coord.wasInjected(protocol.Austin, "wake up") {
		t.Fatal("harness_prompt was not recorded as injected")
	}

	// A response-type message is not injected into the session, so it must not
	// overwrite what was recorded.
	_ = coord.sendToAgent(ctx, protocol.Austin, protocol.Message{
		Type: protocol.MsgResponse, Text: "ack",
	})
	if !coord.wasInjected(protocol.Austin, "wake up") {
		t.Fatal("a response overwrote the recorded injection")
	}
}
