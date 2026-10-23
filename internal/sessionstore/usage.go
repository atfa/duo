package sessionstore

import (
	"fmt"
	"strings"
)

// CostLine is the one spelling of a cost total. The dashboard's cost panel, the
// overview and the coordinator's final summary all quote it, so a human reads the
// same sentence in every place: a total phrased differently per view is a total the
// human has to reconcile by hand.
func (u UsageTotals) CostLine(zh bool) string {
	if zh {
		return fmt.Sprintf("会话成本：$%.2f · %s tokens · %d 轮", u.CostUSD, HumanTokens(u.TotalTokens), u.Turns)
	}
	return fmt.Sprintf("Session cost: $%.2f · %s tokens · %d turns", u.CostUSD, HumanTokens(u.TotalTokens), u.Turns)
}

// HumanTokens renders a token count as the interface does, so the number in a
// sentence an agent reads matches the number on the human's screen.
func HumanTokens(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.1f", float64(n)/1000), "0"), ".") + "k"
}
