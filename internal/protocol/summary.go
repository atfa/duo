package protocol

import "strings"

// The rule Austin prints across the top of its final report, and the test the
// interface uses to recognise that report.
//
// A rule of equals signs is the whole contract for three reasons. It is the one
// line of the box that carries no text, so it reads the same in every language
// the project ships and the interface never has to parse what Austin wrote
// inside the box. It is distinctive enough that an ordinary report — which
// Austin writes whenever it feels like it — is not mistaken for the one the
// human was promised. And the interface's markdown renderer leaves it alone:
// its own rule handling accepts only dashes, asterisks and underscores, so a line
// of equals signs reaches the screen as Austin drew it.
const (
	// SummaryRuleChar is the character the report's rules are drawn with.
	SummaryRuleChar = "="
	// SummaryRuleWidth is how many of them make the rule unmistakable without
	// being wider than a narrow terminal.
	SummaryRuleWidth = 67
	// SummaryRuleMinWidth is the shortest run that counts as a rule. Anything
	// shorter is an ordinary line that happens to contain an equals sign.
	SummaryRuleMinWidth = 8
)

// SummaryRuleLine is the exact rule Austin opens its final report with.
func SummaryRuleLine() string {
	return strings.Repeat(SummaryRuleChar, SummaryRuleWidth)
}

// IsSummaryReport reports whether a message is Austin's final report: the one
// Duo asked for at DONE, as opposed to anything Austin decides to say to the
// human at any other time.
//
// The test is deliberately about shape rather than content. A report that
// arrives without its rule is still the human's report — it is simply not
// marked, which is the failure mode worth having: an unstyled summary is still
// readable, while a misrecognised one would assert something untrue.
func IsSummaryReport(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		return len(trimmed) >= SummaryRuleMinWidth &&
			strings.Trim(trimmed, SummaryRuleChar) == ""
	}
	return false
}
