package remote

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

// The vectors below were computed independently of this package (RFC 5869 HKDF,
// HMAC-SHA256 and AES-256-GCM with a zero IV), so they check the derivation and
// the AEAD parameters rather than merely re-running the implementation. The page
// must produce the same bytes from the same K.
const (
	vectorKey    = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	vectorProof  = "_5LL3yvE9T7Xl6BoJZINPgtr-SikscYPsELlvqDqqoc"
	vectorEncKey = "cHuL5VvcM-gsf8RTpabMoA7aTVrSNqpQ_pK4syfZBlU"
	vectorCmdKey = "b-HHX7hlMWpmmrACZlQvIVDq3JbJ_37nGs3iPIf3oGY"
	vectorCmdMAC = "8yZ0b3JtkHnfvu5JAcpQhGkNfTeLTn-FvBQBcuiG9XQ"
	// One AES-256-GCM ciphertext of {"v":1,"n":1,"ts":0,"project":"duo"} with a
	// zero IV and additional data "room|state|1".
	vectorIV = "AAAAAAAAAAAAAAAA"
	vectorCT = "I2rNLAXdr3JyQ4ho2NzLC25q8jvkbuu2n1qzb8Gw2Qcn1Pe84oiDPR1fAItw13nyFKrzEQ"
)

func vectorPairing() Pairing {
	return Pairing{Server: DefaultServer, Room: "room", Key: vectorKey, ExpiresAt: time.Unix(2_000_000_000, 0)}
}

func TestDerivedValuesMatchDocumentedVectors(t *testing.T) {
	p := vectorPairing()
	proof, err := p.Proof()
	if err != nil {
		t.Fatalf("Proof: %v", err)
	}
	if proof != vectorProof {
		t.Errorf("proof = %s, want %s", proof, vectorProof)
	}
	enc, err := p.encryptKey()
	if err != nil {
		t.Fatalf("encryptKey: %v", err)
	}
	if got := base64.RawURLEncoding.EncodeToString(enc); got != vectorEncKey {
		t.Errorf("encKey = %s, want %s", got, vectorEncKey)
	}
	cmd, err := p.commandKey()
	if err != nil {
		t.Fatalf("commandKey: %v", err)
	}
	if got := base64.RawURLEncoding.EncodeToString(cmd); got != vectorCmdKey {
		t.Errorf("cmdMACKey = %s, want %s", got, vectorCmdKey)
	}
	mac, err := p.SignCommand("id-1", CmdTask, 1, "run the tests")
	if err != nil {
		t.Fatalf("SignCommand: %v", err)
	}
	if mac != vectorCmdMAC {
		t.Errorf("command MAC = %s, want %s", mac, vectorCmdMAC)
	}
}

func TestOpenMessageAcceptsTheDocumentedCiphertext(t *testing.T) {
	p := vectorPairing()
	plaintext, err := p.OpenMessage(KindState, 1, vectorIV, vectorCT)
	if err != nil {
		t.Fatalf("OpenMessage: %v", err)
	}
	if want := `{"v":1,"n":1,"ts":0,"project":"duo"}`; string(plaintext) != want {
		t.Errorf("plaintext = %s, want %s", plaintext, want)
	}
}

func TestOpenMessageBindsKindAndSequence(t *testing.T) {
	p := vectorPairing()
	if _, err := p.OpenMessage(KindEvent, 1, vectorIV, vectorCT); err == nil {
		t.Error("a state ciphertext opened as an event; the additional data does not bind the kind")
	}
	if _, err := p.OpenMessage(KindState, 2, vectorIV, vectorCT); err == nil {
		t.Error("a state ciphertext opened at another sequence; the additional data does not bind the seq")
	}
	other := vectorPairing()
	other.Room = "other"
	if _, err := other.OpenMessage(KindState, 1, vectorIV, vectorCT); err == nil {
		t.Error("a state ciphertext opened in another room; the additional data does not bind the room")
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	p, err := NewPairing(DefaultServer, time.Now(), DefaultRoomTTL)
	if err != nil {
		t.Fatalf("NewPairing: %v", err)
	}
	iv, ct, err := p.SealMessage(KindState, 7, []byte("hello phone"))
	if err != nil {
		t.Fatalf("SealMessage: %v", err)
	}
	got, err := p.OpenMessage(KindState, 7, iv, ct)
	if err != nil {
		t.Fatalf("OpenMessage: %v", err)
	}
	if string(got) != "hello phone" {
		t.Errorf("round trip = %q", got)
	}
	// A different pairing must not open it.
	other, _ := NewPairing(DefaultServer, time.Now(), DefaultRoomTTL)
	if _, err := other.OpenMessage(KindState, 7, iv, ct); err == nil {
		t.Error("another pairing opened the ciphertext")
	}
}

func TestCommandMACRejectsTampering(t *testing.T) {
	p, err := NewPairing(DefaultServer, time.Now(), DefaultRoomTTL)
	if err != nil {
		t.Fatalf("NewPairing: %v", err)
	}
	mac, err := p.SignCommand("id-1", CmdTask, 4, "run the tests")
	if err != nil {
		t.Fatalf("SignCommand: %v", err)
	}
	if err := p.VerifyCommand("id-1", CmdTask, 4, "run the tests", mac); err != nil {
		t.Fatalf("VerifyCommand: %v", err)
	}
	cases := []struct {
		name      string
		id, kind  string
		n         int64
		text, mac string
	}{
		{"other text", "id-1", CmdTask, 4, "delete the repo", mac},
		{"other kind", "id-1", CmdNewSession, 4, "run the tests", mac},
		{"other nonce", "id-1", CmdTask, 5, "run the tests", mac},
		{"other id", "id-2", CmdTask, 4, "run the tests", mac},
		{"truncated mac", "id-1", CmdTask, 4, "run the tests", mac[:20]},
	}
	for _, tc := range cases {
		if err := p.VerifyCommand(tc.id, tc.kind, tc.n, tc.text, tc.mac); err == nil {
			t.Errorf("%s: MAC verified, want rejection", tc.name)
		}
	}
}

func TestPairingURLKeepsTheSecretInTheFragment(t *testing.T) {
	p := Pairing{Server: "https://remote.example", Room: "r", Key: vectorKey}
	got := p.URL()
	if got != "https://remote.example/p/r#"+vectorKey {
		t.Fatalf("URL = %q", got)
	}
	before, _, ok := strings.Cut(got, "#")
	if !ok {
		t.Fatal("URL has no fragment")
	}
	if strings.Contains(before, vectorKey) {
		t.Error("the secret appears before the fragment, so a server would receive it")
	}
}

func TestValidProofAndRoom(t *testing.T) {
	p, err := NewPairing(DefaultServer, time.Now(), DefaultRoomTTL)
	if err != nil {
		t.Fatalf("NewPairing: %v", err)
	}
	proof, _ := p.Proof()
	if !ValidProof(proof) {
		t.Error("a generated proof was rejected")
	}
	if !ValidRoom(p.Room) {
		t.Error("a generated room id was rejected")
	}
	for _, bad := range []string{"", "short", strings.Repeat("A", 44), strings.Repeat("!", 43), proof + "="} {
		if ValidProof(bad) {
			t.Errorf("ValidProof(%q) = true", bad)
		}
	}
	for _, bad := range []string{"", "room", strings.Repeat("A", 21), strings.Repeat("A", 23), strings.Repeat("+", 22)} {
		if ValidRoom(bad) {
			t.Errorf("ValidRoom(%q) = true", bad)
		}
	}
}

func TestNormalizeServerAndRoomFromURL(t *testing.T) {
	if got := NormalizeServer(" https://remote.xduo.dev/ "); got != "https://remote.xduo.dev" {
		t.Errorf("NormalizeServer = %q", got)
	}
	p, err := NewPairing(DefaultServer, time.Now(), DefaultRoomTTL)
	if err != nil {
		t.Fatalf("NewPairing: %v", err)
	}
	if got := RoomFromURL(p.URL()); got != p.Room {
		t.Errorf("RoomFromURL(URL) = %q, want %q", got, p.Room)
	}
	if got := RoomFromURL(p.Server + "/p/" + p.Room); got != p.Room {
		t.Errorf("RoomFromURL(no fragment) = %q", got)
	}
	if got := RoomFromURL("https://remote.xduo.dev/p/not-a-room#x"); got != "" {
		t.Errorf("RoomFromURL(malformed) = %q, want empty", got)
	}
}

func TestPairingLiveness(t *testing.T) {
	now := time.Now()
	p, err := NewPairing(DefaultServer, now, time.Hour)
	if err != nil {
		t.Fatalf("NewPairing: %v", err)
	}
	if !p.Live(now) {
		t.Error("a fresh pairing is not live")
	}
	if p.Live(now.Add(2 * time.Hour)) {
		t.Error("an expired pairing is live")
	}
	p.Revoked = true
	if p.Live(now) {
		t.Error("a revoked pairing is live")
	}
}
