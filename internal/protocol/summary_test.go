package protocol

import "testing"

// IsSummaryReport is the whole mechanism by which the interface tells Austin's
// promised report apart from anything Austin decides to say on its own, so the
// shape it accepts has to be exactly the shape the prompt asks for.
func TestIsSummaryReportRecognisesTheRuleAustinIsAskedToDraw(t *testing.T) {
	rule := SummaryRuleLine()
	report := rule + "\n|  DUO SESSION SUMMARY — AUSTIN  |\n" + rule + "\n\n1. Result. …"
	if !IsSummaryReport(report) {
		t.Fatalf("the report the prompt asks for is not recognised:\n%s", report)
	}
	// Leading whitespace from a copied reply must not hide it.
	if !IsSummaryReport("\n\n" + rule + "\nbody") {
		t.Fatal("blank lines before the rule must not hide the report")
	}
}

func TestIsSummaryReportDoesNotClaimAnOrdinaryMessage(t *testing.T) {
	for _, text := range []string{
		"",
		"Delivery confirmed — master is now 326e6fc.",
		// The shape is close enough that only the width keeps them apart.
		"=====",
		// A rule drawn with the characters the interface renders as a
		// horizontal rule is not the report's own marker.
		"--------",
		// Prose that happens to start with an equals sign is prose.
		"= 1. Result: nothing changed.",
	} {
		if IsSummaryReport(text) {
			t.Errorf("ordinary message claimed as a summary report: %q", text)
		}
	}
}
