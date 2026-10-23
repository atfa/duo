package coordinator

import (
	"context"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/agent"
	"github.com/atfa/duo/internal/agent/agenttest"
	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
)

// TestCutUnavailableToolsDropsTheWholeInstruction is the point of the feature: a
// sentence that names a tool the agent cannot call is an instruction it cannot
// follow, so it leaves with the tool rather than as a sentence with no verb.
func TestCutUnavailableToolsDropsTheWholeInstruction(t *testing.T) {
	text := "Both approve the queue with duo_set_status ready=true. A [fast] step is Austin implementing and Tony verifying with duo_set_verification. Call duo_status first."
	caps := driver.Capabilities{Tools: []string{"duo_status", "duo_set_status"}}

	got := cutUnavailableTools(text, caps)

	if strings.Contains(got, "duo_set_verification") {
		t.Errorf("prompt still promises a tool the driver does not expose: %q", got)
	}
	for _, want := range []string{"duo_set_status", "duo_status", "Both approve the queue"} {
		if !strings.Contains(got, want) {
			t.Errorf("filtered prompt lost %q: %q", want, got)
		}
	}
}

// TestCutUnavailableToolsKeepsEverythingForAnUndeclaringDriver is the conservative
// half: a driver that says nothing about its tools keeps the full contract, because
// the cost of the assumption is one sentence the agent can ignore.
func TestCutUnavailableToolsKeepsEverythingForAnUndeclaringDriver(t *testing.T) {
	text := "Call duo_status, then duo_set_steps, then duo_restart_duo."
	if got := cutUnavailableTools(text, driver.Capabilities{}); got != text {
		t.Errorf("an undeclared tool set changed the prompt: %q", got)
	}
}

// TestCutUnavailableToolsSplitsChineseSentences matters because Duo's prompts are
// bilingual: a filter that only knows ". " as a boundary would drop whole Chinese
// paragraphs, or none of them.
func TestCutUnavailableToolsSplitsChineseSentences(t *testing.T) {
	text := "双方用 duo_set_status ready=true 批准该精确队列。FAST 步骤由 Austin 实现、Tony 用 duo_set_verification 独立验证。先调用 duo_status。"
	caps := driver.Capabilities{Tools: []string{"duo_status", "duo_set_status"}}

	got := cutUnavailableTools(text, caps)

	if strings.Contains(got, "duo_set_verification") {
		t.Errorf("Chinese sentence naming an unavailable tool survived: %q", got)
	}
	for _, want := range []string{"duo_set_status", "duo_status"} {
		if !strings.Contains(got, want) {
			t.Errorf("filtered Chinese prompt lost %q: %q", want, got)
		}
	}
}

// TestSplitSentencesKeepsTextIntact guards the rejoin: a filter that mangled
// punctuation would rewrite every prompt it passes through.
func TestSplitSentencesKeepsTextIntact(t *testing.T) {
	for _, text := range []string{
		"First. Second.\nThird line\n",
		"Runs Duo 1.5 with v2 semantics; then exits.",
		"中文句子。第二句；第三句\n",
	} {
		if got := strings.Join(splitSentences(text), ""); got != text {
			t.Errorf("split and rejoin changed the text:\n got %q\nwant %q", got, text)
		}
	}
}

// TestFilterToolMentionsUsesTheReceivingAgentDriver is the wiring check: the set
// comes from the manifest of the agent the prompt is addressed to, so two agents on
// different drivers get the wording each of them can act on.
func TestFilterToolMentionsUsesTheReceivingAgentDriver(t *testing.T) {
	ctx := context.Background()
	stub := agenttest.New("stub").With(func(spec *agenttest.Spec) {
		spec.Capabilities.Tools = []string{"duo_status"}
	})
	session, err := agent.NewSession(ctx, agent.Config{
		Agent: protocol.Austin, Dir: t.TempDir(), Plugin: stub.Caller(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	manager := agent.NewManager()
	manager.Add(session)
	c := New(nil, project.NewState(), harness.NewTracker(), nil, events.NewBus())
	c.SetAgents(manager)

	text := "Sign with duo_set_status. Read state with duo_status."
	if got := c.filterToolMentions(protocol.Austin, text); strings.Contains(got, "duo_set_status") {
		t.Errorf("prompt kept a tool the agent's driver does not expose: %q", got)
	}
	// Tony has no driver at all, so Core knows nothing about his tools and keeps
	// every sentence.
	if got := c.filterToolMentions(protocol.Tony, text); got != text {
		t.Errorf("an agent with no driver must keep the full prompt: %q", got)
	}
}
