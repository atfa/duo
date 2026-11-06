// Package cellwidth pins go-runewidth to the cells tcell paints.
//
// Every frontend places text in cells, so a width it measures has to match the
// cells the text is actually written into. go-runewidth's package default
// follows the locale: on a zh_CN host it counts East Asian ambiguous characters —
// the bullet, the quote gutter, the rule, the ellipsis both TUIs draw — as two
// cells while the terminal paints them in one. Everything drawn after such a
// character then shifts right, and hit rectangles and truncation limits drift
// from the picture.
//
// Pinning the table to one cell for ambiguous characters is the only place both
// layers read, so measurement and placement agree by construction. It mirrors
// tcell's own switch (see tcell's eastasian.go): a host that asks for two-cell
// ambiguous characters gets two cells from both. Both frontends import this
// package for the init, so cmd/duo and cmd/xduo cannot drift apart.
package cellwidth

import (
	"os"
	"strings"

	rw "github.com/mattn/go-runewidth"
)

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
