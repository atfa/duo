package gotui

import (
	"os"
	"strings"

	rw "github.com/mattn/go-runewidth"
)

// The interface places text in cells, so every width it computes has to match
// the cells the text is actually written into.
//
// gotui's Buffer.SetString advances with go-runewidth's package default, which
// follows the locale: on a zh_CN host it counts East Asian ambiguous characters —
// the bullet, the quote gutter, the rule, the ellipsis this frontend draws — as
// two cells while the terminal paints them in one. The buffer then leaves a blank
// cell after each of them, so bullets showed a double gap, rules showed a gap
// between dashes, and everything drawn after such a character was shifted right.
//
// Pinning the table to one cell for ambiguous characters is the only place both
// layers read, so the measurement and the placement agree by construction. It
// mirrors tcell's own switch (see tcell's eastasian.go): a host that asks for
// two-cell ambiguous characters gets two cells from both.
func init() {
	want := eastAsianAmbiguous()
	if rw.DefaultCondition.EastAsianWidth == want {
		return
	}
	rw.DefaultCondition.EastAsianWidth = want
	rw.EastAsianWidth = want
	// RuneWidth prefers a lookup table, and the one built for the host's answer
	// would keep giving that answer; rebuilding it is the only way to change it.
	// It is rebuilt in place, so this costs nothing but the pass over the table.
	rw.DefaultCondition.CreateLUT()
}

// eastAsianAmbiguous reads the same environment switch tcell reads.
func eastAsianAmbiguous() bool {
	switch strings.ToLower(os.Getenv("RUNEWIDTH_EASTASIAN")) {
	case "1", "true", "yes":
		return true
	}
	return false
}
