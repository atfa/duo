package gotui

import (
	"fmt"
	"io"
	"strings"

	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/terminal"
)

// attentionTitle is the notification title. It goes through theme.tr, so the
// text follows the session's /language setting like every other label.
const attentionTitle = "Duo needs you"

// attentionBody names what the human must look at. The reason codes are the
// coordinator's, not the dashboard's: the session owns the state that makes a
// human needed, and a notification is that state's output projection, so no
// counter, flag, or phase is added here. An unknown code produces no
// notification: a code the dashboard does not understand is not a reason to
// interrupt anyone.
func attentionBody(reason string) string {
	switch reason {
	case events.AttentionConflict:
		return "Integration hit a merge conflict; the session is waiting for you."
	case events.AttentionDelivery:
		return "Delivery was rejected; the finished work is waiting for you."
	case events.AttentionVerification:
		return "A Fast verification is waiting; the session is paused in VERIFICATION."
	case events.AttentionSummary:
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

// notifyHuman projects one attention event onto the terminal. The operator's
// notify switch is read from the session view, so one configuration governs
// every frontend; when it is off, nothing is written at all. The write goes to
// /dev/tty, the same channel the dashboard renders on: a notification belongs to
// the terminal the session is running in, not to whatever stdout was redirected
// to.
func (a *App) notifyHuman(reason string) {
	if !a.live.Notify {
		return
	}
	body := attentionBody(reason)
	if body == "" {
		return
	}
	if a.notifyTTY == nil {
		openTTY := a.openTTY
		if openTTY == nil {
			openTTY = terminal.Open
		}
		tty, err := openTTY()
		if err != nil {
			return
		}
		a.notifyTTY = tty
	}
	_ = writeAttention(a.notifyTTY.File, a.theme.tr(attentionTitle), a.theme.tr(body))
}
