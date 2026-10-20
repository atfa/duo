package gotui

import (
	"unicode/utf8"

	"github.com/gdamore/tcell/v3"
	"golang.org/x/text/encoding"
	"golang.org/x/text/transform"
)

// tcell v3 decodes every terminal read with atEOF=true. Its UTF-8 validator
// treats a rune split across reads as invalid and leaves it unconsumed, which
// can corrupt Chinese IME input and spin tcell's input loop. Register a
// streaming decoder that leaves an incomplete rune buffered for the next read.
func init() {
	tcell.RegisterEncoding("utf-8", utf8StreamEncoding{})
	tcell.RegisterEncoding("utf8", utf8StreamEncoding{})
}

type utf8StreamEncoding struct{}

func (utf8StreamEncoding) NewDecoder() *encoding.Decoder {
	return &encoding.Decoder{Transformer: utf8StreamDecoder{}}
}

func (utf8StreamEncoding) NewEncoder() *encoding.Encoder {
	return encoding.Nop.NewEncoder()
}

type utf8StreamDecoder struct{}

func (utf8StreamDecoder) Reset() {}

// Transform ignores atEOF: tcell marks every terminal read as final, but a read
// boundary is not the end of the input stream.
func (utf8StreamDecoder) Transform(dst, src []byte, _ bool) (nDst, nSrc int, err error) {
	for nSrc < len(src) {
		if src[nSrc] < utf8.RuneSelf {
			if nDst >= len(dst) {
				return nDst, nSrc, transform.ErrShortDst
			}
			dst[nDst] = src[nSrc]
			nDst++
			nSrc++
			continue
		}
		if !utf8.FullRune(src[nSrc:]) {
			return nDst, nSrc, transform.ErrShortSrc
		}
		r, size := utf8.DecodeRune(src[nSrc:])
		if r == utf8.RuneError && size == 1 {
			const replacement = "\uFFFD"
			if len(dst)-nDst < len(replacement) {
				return nDst, nSrc, transform.ErrShortDst
			}
			nDst += copy(dst[nDst:], replacement)
			nSrc++
			continue
		}
		if len(dst)-nDst < size {
			return nDst, nSrc, transform.ErrShortDst
		}
		nDst += copy(dst[nDst:], src[nSrc:nSrc+size])
		nSrc += size
	}
	return nDst, nSrc, nil
}
