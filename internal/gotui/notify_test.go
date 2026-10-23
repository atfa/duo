package gotui

import (
	"bytes"
	"testing"
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
func TestAttentionBodyCoversTheFourReasonCodes(t *testing.T) {
	for _, reason := range []string{attentionConflict, attentionDelivery, attentionVerification, attentionSummary} {
		if attentionBody(reason) == "" {
			t.Errorf("reason %q has no notification text", reason)
		}
	}
	if attentionBody("nonsense") != "" {
		t.Error("an unknown reason code must produce no notification")
	}
}
