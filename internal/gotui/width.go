package gotui

// The reasoning for this pin lives in cellwidth: gotui's Buffer.SetString and
// tcell both advance with go-runewidth, and on a zh_CN host its default counts
// East Asian ambiguous characters as two cells while the terminal paints one.
import _ "github.com/atfa/duo/internal/cellwidth"
