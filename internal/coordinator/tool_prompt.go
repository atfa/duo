package coordinator

import (
	"strings"

	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/internal/mcp"
	"github.com/atfa/duo/internal/protocol"
)

// filterToolMentions cuts a Core-authored prompt down to the coordination tools the
// receiving agent can actually call.
//
// The failure it prevents is one an agent cannot report: a prompt that instructs it
// to call a tool its driver never exposed. The agent searches for a tool that is
// not there, and the session pays for the turn. Core knows what a driver exposes
// because the driver declares it in `capabilities.tools`, so the wording can be cut
// to what is reachable.
//
// The cut is per sentence rather than per word because a tool name inside a
// sentence carries the instruction with it: removing "duo_set_steps" from "publish
// the queue with duo_set_steps" leaves an instruction with no verb. A sentence that
// names an unavailable tool is a sentence the agent cannot act on, so it goes.
//
// A driver that declares no tools keeps every sentence. See driver.Capabilities.HasTool
// for why unknown is treated as able.
func (c *Coordinator) filterToolMentions(agent protocol.AgentID, text string) string {
	if text == "" || c.agents == nil {
		return text
	}
	manifest := c.agents.ManifestFor(agent)
	if manifest == nil {
		return text
	}
	return cutUnavailableTools(text, manifest.Capabilities)
}

// cutUnavailableTools drops every sentence of text that names a tool the driver did
// not declare. It is split out so the rule can be tested without a session.
func cutUnavailableTools(text string, caps driver.Capabilities) string {
	if len(caps.Tools) == 0 {
		return text
	}
	var out strings.Builder
	for _, segment := range splitSentences(text) {
		if namesUnavailableTool(segment, caps) {
			continue
		}
		out.WriteString(segment)
	}
	return collapseBlankLines(out.String())
}

func namesUnavailableTool(segment string, caps driver.Capabilities) bool {
	for _, name := range mcp.ToolNames() {
		if strings.Contains(segment, name) && !caps.HasTool(name) {
			return true
		}
	}
	return false
}

// sentenceEnders are the characters that end a clause in the prompt text, which is
// written in both English and Chinese. Commas count: a clause is the unit an
// instruction fits in, so cutting at one removes only what the agent cannot act on.
const sentenceEnders = "。；;，,！!？?\n"

// splitSentences divides text into segments at clause boundaries, keeping each
// boundary with the segment that ends at it so the segments rejoin unchanged.
//
// A period only ends a clause when whitespace follows it, so a version, a decimal
// and an initialism survive intact.
func splitSentences(text string) []string {
	var out []string
	start := 0
	for i, r := range text {
		if !endsClause(text, i, r) {
			continue
		}
		end := i + len(string(r))
		out = append(out, text[start:end])
		start = end
	}
	if start < len(text) {
		out = append(out, text[start:])
	}
	return out
}

func endsClause(text string, i int, r rune) bool {
	if strings.ContainsRune(sentenceEnders, r) {
		return true
	}
	if r != '.' {
		return false
	}
	next := i + 1 // '.' is one byte, so the next byte is the next character.
	return next == len(text) || text[next] == ' ' || text[next] == '\n'
}

// collapseBlankLines removes the vertical gaps a dropped sentence leaves behind, so
// a filtered prompt reads as written rather than as a text with holes in it.
func collapseBlankLines(text string) string {
	for strings.Contains(text, "\n\n\n") {
		text = strings.ReplaceAll(text, "\n\n\n", "\n\n")
	}
	return strings.TrimRight(text, " \n")
}
