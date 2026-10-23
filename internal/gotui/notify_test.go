package gotui

import (
	"bytes"
	"os"
	"testing"

	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/terminal"
)

// TestHumanAttentionEmitsBellAndOSC777 pins the exact byte sequence, because a
// terminal matches escape sequences byte for byte: a reformatted OSC 777 is not
// a notification with a cosmetic problem, it is no notification at all.
func TestHumanAttentionEmitsBellAndOSC777(t *testing.T) {
	var out bytes.Buffer
	if err := writeAttention(&out, "Duo needs you", "Integration hit a merge conflict; the session is waiting for you."); err != nil {
		t.Fatal(err)
	}
	want := "\a\x1b]777;notify;Duo needs you;Integration hit a merge conflict; the session is waiting for you.\a"
	if got := out.String(); got != want {
		t.Errorf("notification bytes = %q\nwant %q", got, want)
	}
}

// TestWriteAttentionStripsSequenceTerminators: a bell inside the body would end
// the OSC string early and print the rest as text on the user's screen.
func TestWriteAttentionStripsSequenceTerminators(t *testing.T) {
	var out bytes.Buffer
	if err := writeAttention(&out, "a\ab", "c\nd\033e"); err != nil {
		t.Fatal(err)
	}
	want := "\a\x1b]777;notify;ab;c de\a"
	if got := out.String(); got != want {
		t.Errorf("notification bytes = %q\nwant %q", got, want)
	}
}

// TestAttentionBodyCoversTheFourReasonCodes is the contract with the coordinator:
// every reason code it emits has text, and an unknown code interrupts nobody.
// The codes come from the events package, so a Core rename breaks this test at
// compile time instead of silently dropping notifications.
func TestAttentionBodyCoversTheFourReasonCodes(t *testing.T) {
	for _, reason := range []string{events.AttentionConflict, events.AttentionDelivery, events.AttentionVerification, events.AttentionSummary} {
		if attentionBody(reason) == "" {
			t.Errorf("reason %q has no notification text", reason)
		}
	}
	if attentionBody("nonsense") != "" {
		t.Error("an unknown reason code must produce no notification")
	}
}

// notifyAppForTest is an App whose notification terminal is a temp file, so a
// test can assert on the exact bytes the dashboard wrote.
func notifyAppForTest(t *testing.T, notify bool) (*App, *os.File) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "notify")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	a := &App{}
	a.live.Notify = notify
	a.openTTY = func() (*terminal.TTY, error) { return &terminal.TTY{File: f}, nil }
	return a, f
}

// TestNotifyDisabledByConfig is the operator's switch: the session view carries
// the resolved value of the `notify` configuration, and off means the dashboard
// writes nothing to the terminal at all.
func TestNotifyDisabledByConfig(t *testing.T) {
	a, f := notifyAppForTest(t, false)
	a.notifyHuman(events.AttentionConflict)
	if data, _ := os.ReadFile(f.Name()); len(data) != 0 {
		t.Errorf("notify off wrote %q to the terminal", data)
	}

	a, f = notifyAppForTest(t, true)
	a.notifyHuman(events.AttentionConflict)
	data, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	want := "\a\x1b]777;notify;Duo needs you;Integration hit a merge conflict; the session is waiting for you.\a"
	if string(data) != want {
		t.Errorf("notify on wrote %q\nwant %q", data, want)
	}
}

// TestNotifyTextFollowsLanguage is the /language contract: the notification text
// is translated through the same table as the rest of the dashboard chrome.
func TestNotifyTextFollowsLanguage(t *testing.T) {
	zh := "zh"
	a, f := notifyAppForTest(t, true)
	a.theme.Lang = &zh
	a.notifyHuman(events.AttentionSummary)
	data, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	want := "\a\x1b]777;notify;Duo 需要你;最终摘要已就绪；会话正在等你查看。\a"
	if string(data) != want {
		t.Errorf("zh notification = %q\nwant %q", data, want)
	}
}
