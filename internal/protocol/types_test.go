package protocol

import "testing"

func TestCanonicalAgentAndPeer(t *testing.T) {
	if got := CanonicalAgent(" aUsTiN "); got != Austin {
		t.Fatalf("CanonicalAgent = %q, want %q", got, Austin)
	}
	if got := PeerOf(Austin); got != Tony {
		t.Fatalf("PeerOf(Austin) = %q, want %q", got, Tony)
	}
	if got := PeerOf(Tony); got != Austin {
		t.Fatalf("PeerOf(Tony) = %q, want %q", got, Austin)
	}
}
