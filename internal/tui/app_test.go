package tui

import "testing"

func TestNativeDetach(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		detach  bool
		forward int
	}{
		{"legacy", "\x1d", true, 0},
		{"csi-u", "\x1b[93;5u", true, len("\x1b[93;5u")},
		{"modifyOtherKeys", "\x1b[27;5;93~", true, len("\x1b[27;5;93~")},
		{"similar CSI-u", "\x1b[93;4u", false, len("\x1b[93;4u")},
		{"ordinary input", "hello", false, len("hello")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &App{}
			var got bool
			forwarded := 0
			for _, b := range []byte(tt.input) {
				detach, forward := a.nativeDetach(b)
				got = got || detach
				if forward {
					forwarded++
				}
			}
			if got != tt.detach || forwarded != tt.forward {
				t.Fatalf("detach=%v, forwarded=%d; want detach=%v, forwarded=%d", got, forwarded, tt.detach, tt.forward)
			}
		})
	}
}
