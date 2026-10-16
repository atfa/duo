package project

import (
	"fmt"
	"regexp"
	"strings"
)

// The Step queue is Step-mode state, not plan prose. Agents author it as a
// Markdown checkbox list; Duo parses it once at the boundary, owns the ticks, and
// renders the array back into a checkbox list wherever a prompt needs one. The
// array is the only source of truth, so a plan edit can never move the cursor by
// editing text, and Duo can record per-step evidence text cannot carry.

// Step is one unit of work in a Step-mode queue.
type Step struct {
	Title string `json:"title"`
	Done  bool   `json:"done"`

	// Head is the Austin artifact Tony reviewed for this step, and Verification is
	// Tony's verdict bound to it. Both are per-step so a step can be audited after
	// the session is long finished.
	Head         string       `json:"head,omitempty"`
	Verification Verification `json:"verification,omitempty"`

	// DeliveryHead and DeliveredAt record where this step landed in the user's
	// repository. A step is landed by the ordinary delivery transaction, so this is
	// the durable proof that the user received it.
	DeliveryHead string `json:"deliveryHead,omitempty"`
	DeliveredAt  string `json:"deliveredAt,omitempty"`
}

// checkboxLine matches the Markdown task item Duo reads as one step. It accepts
// the `-`, `*` and `+` bullets and both checkbox states.
var checkboxLine = regexp.MustCompile(`^\s*[-*+] \[( |x|X)\] (.*)$`)

// Steps is a Step-mode queue. It is a named slice so the renderers are methods on
// the queue: prompts, the TUI and duo_status all call the same two functions, so
// no surface can drift from the others.
type Steps []Step

// Render is the full queue: one checkbox line per step, with where a landed step
// went and what is under review. This is what a phase or resume prompt injects,
// once per transition.
func (s Steps) Render() string {
	var b strings.Builder
	for i, st := range s {
		box := " "
		if st.Done {
			box = "x"
		}
		fmt.Fprintf(&b, "%d. [%s] %s", i+1, box, st.Title)
		if st.DeliveryHead != "" {
			fmt.Fprintf(&b, " — landed %s", shortHash(st.DeliveryHead))
		} else if st.Head != "" {
			fmt.Fprintf(&b, " — submitted %s", shortHash(st.Head))
			if st.Verification.Status != "" {
				fmt.Fprintf(&b, ", review %s", st.Verification.Status)
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

// RenderCursor is the one-line queue state a repeating nudge can afford: the
// cursor, the current step's title, and one short line per remaining step. It
// never carries step bodies, for the reason NudgeState gives for the plan: a nudge
// repeats on a timer, and over a ten-step loop the bodies are the dominant token
// cost.
func (s Steps) RenderCursor() string {
	open := FirstOpenStep(s)
	var b strings.Builder
	if open > len(s) {
		fmt.Fprintf(&b, "step queue: %d/%d landed\n", len(s), len(s))
		return b.String()
	}
	current := s[open-1]
	fmt.Fprintf(&b, "step %d/%d: %s", open, len(s), current.Title)
	if current.DeliveryHead != "" {
		fmt.Fprintf(&b, " [landed %s]", shortHash(current.DeliveryHead))
	}
	b.WriteString("\n")
	for i := open; i < len(s); i++ {
		fmt.Fprintf(&b, "- step %d: %s\n", i+1, s[i].Title)
	}
	return b.String()
}

// ParseSteps turns a Markdown checkbox list into a queue. Lines that are not
// checkbox items are ignored: the queue is the list, not the whole plan.
func ParseSteps(list string) Steps {
	var steps Steps
	for _, line := range strings.Split(list, "\n") {
		m := checkboxLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		title := strings.TrimSpace(m[2])
		if title == "" {
			continue
		}
		steps = append(steps, Step{
			Title: title,
			Done:  strings.EqualFold(strings.TrimSpace(m[1]), "x"),
		})
	}
	return steps
}

// FirstOpenStep returns the 1-based index of the first step that is not done, or
// len(steps)+1 when the queue is finished.
func FirstOpenStep(steps Steps) int {
	for i, s := range steps {
		if !s.Done {
			return i + 1
		}
	}
	return len(steps) + 1
}

// QueueFinished reports whether every step has landed.
func QueueFinished(steps Steps) bool {
	for _, s := range steps {
		if !s.Done {
			return false
		}
	}
	return len(steps) > 0
}
