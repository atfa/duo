package gotui

import (
	"fmt"
	"io"
	"strings"
)

// The attention reason codes are the coordinator's, not the dashboard's: the
// dashboard only projects them. The session owns the state that makes a human
// needed; a notification is that state's output, so no counter, flag, or phase
// is added here.
const (
	attentionConflict     = "conflict"
	attentionDelivery     = "delivery"
	attentionVerification = "verification"
	attentionSummary      = "summary"
)

// attentionTitle is the notification title. It goes through theme.tr, so the
// text follows the session's /language setting like every other label.
const attentionTitle = "Duo needs you"

// attentionBody names what the human must look at. An unknown code produces no
// notification: a code the dashboard does not understand is not a reason to
// interrupt anyone.
func attentionBody(reason string) string {
	switch reason {
	case attentionConflict:
		return "Integration hit a merge conflict; the session is waiting for you."
	case attentionDelivery:
		return "Delivery was rejected; the finished work is waiting for you."
	case attentionVerification:
		return "A Fast verification is waiting; the session is paused in VERIFICATION."
	case attentionSummary:
		return "The final summary is ready; the session is waiting for you."
	}
	return ""
}

// writeAttention emits one desktop notification: BEL, which every terminal
// answers with a beep, followed by OSC 777, which iTerm2, ghostty and Kitty
// raise as a real desktop notification. Terminals that do not implement OSC 777
// ignore it, so the bell is the floor and the notification is the ceiling.
//
// Title and body are stripped of the characters that would terminate the
// sequence early: a bell or escape inside the text would split one notification
// into garbage.
func writeAttention(w io.Writer, title, body string) error {
	clean := strings.NewReplacer("\a", "", "\x1b", "", "\n", " ", "\r", " ")
	_, err := fmt.Fprintf(w, "\a\x1b]777;notify;%s;%s\a", clean.Replace(title), clean.Replace(body))
	return err
}
