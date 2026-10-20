package gotui

import (
	"testing"

	"golang.org/x/text/transform"
)

func TestUTF8StreamDecoderKeepsSplitRunes(t *testing.T) {
	dec := utf8StreamEncoding{}.NewDecoder()
	var pending, out []byte

	for i, b := range []byte("中") {
		pending = append(pending, b)
		dst := make([]byte, 8)
		nOut, nIn, err := dec.Transform(dst, pending, true)
		out = append(out, dst[:nOut]...)
		pending = pending[nIn:]
		if i < 2 {
			if err != transform.ErrShortSrc || nOut != 0 || len(pending) != i+1 {
				t.Fatalf("byte %d: out=%q pending=%q err=%v", i, out, pending, err)
			}
			continue
		}
		if err != nil || string(out) != "中" || len(pending) != 0 {
			t.Fatalf("complete rune: out=%q pending=%q err=%v", out, pending, err)
		}
	}

	// A complete ASCII rune before an incomplete rune must still be consumed.
	pending = []byte{'x', 0xe4}
	dst := make([]byte, 8)
	nOut, nIn, err := dec.Transform(dst, pending, true)
	if got := string(dst[:nOut]); got != "x" || err != transform.ErrShortSrc {
		t.Fatalf("prefix: out=%q err=%v", got, err)
	}
	pending = append(pending[nIn:], 0xb8, 0xad)
	dst = make([]byte, 8)
	nOut, nIn, err = dec.Transform(dst, pending, true)
	if got := string(dst[:nOut]); got != "中" || err != nil || nIn != len(pending) {
		t.Fatalf("completed prefix: out=%q nIn=%d err=%v", got, nIn, err)
	}

	// Malformed input must not wedge the decoder.
	dst = make([]byte, 8)
	nOut, nIn, err = dec.Transform(dst, []byte{0xff}, true)
	if got := string(dst[:nOut]); got != "\uFFFD" || err != nil || nIn != 1 {
		t.Fatalf("invalid byte: out=%q nIn=%d err=%v", got, nIn, err)
	}
}
