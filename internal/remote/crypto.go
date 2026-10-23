// Package remote carries one Duo session to a phone through a content-blind
// relay.
//
// The relay (a Cloudflare Worker in front of a Durable Object) stores and
// forwards ciphertext envelopes. It never receives the pairing secret, a prompt,
// an agent message or a repository path, so it can refuse or delay service but
// cannot read or forge content. The wire format and its limits are specified in
// docs/remote-protocol.md.
//
// This package owns only the transport and the cryptography. It imports no
// session state: the caller supplies a snapshot and implements a small sink for
// the commands a guest sends back.
package remote

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// Subprotocol is the WebSocket subprotocol a client offers. A browser rejects
	// a 101 response that does not select one of the offered subprotocols, so both
	// the page and this client offer it.
	Subprotocol = "duo.v1"

	// DefaultServer is the shared relay. A user who deploys their own sets
	// remote.url in the config file or DUO_REMOTE_URL.
	DefaultServer = "https://remote.xduo.dev"

	// MaxRoomTTL is the longest pairing the relay keeps; it clamps a requested TTL
	// to this value rather than trusting it.
	MaxRoomTTL = 8 * time.Hour

	// DefaultRoomTTL is what a fresh pairing asks for.
	DefaultRoomTTL = MaxRoomTTL

	keyBytes  = 32
	roomBytes = 16
	nonceSize = 12

	proofDomain    = "duo-remote-auth-v1"
	kdfSalt        = "duo-remote-v1"
	kdfInfoEncrypt = "duo-remote-aes-256-gcm"
	kdfInfoCommand = "duo-remote-cmd-mac"

	// proofLength is the length of a base64url-encoded HMAC-SHA256 tag. Both sides
	// validate it before comparing, so a malformed proof never reaches the
	// comparison.
	proofLength = 43
	// roomLength is the length of a base64url-encoded 16-byte room id.
	roomLength = 22
)

// encoding is unpadded base64url everywhere: JSON-safe, URL-safe and
// fixed-length for a fixed-size input.
var encoding = base64.RawURLEncoding

// ErrExpired reports that a pairing outlived its TTL.
var ErrExpired = errors.New("remote: pairing expired")

// ErrRevoked reports that a pairing was revoked on the relay or locally.
var ErrRevoked = errors.New("remote: pairing revoked")

// Pairing is one trust relationship between this Duo session and a phone.
//
// Key is the pairing secret. It is the only value that must never leave the
// machine except inside the QR code fragment, and it is stored owner-only in
// ~/.duo/remote.json so a relaunched session can resume the same room.
type Pairing struct {
	Server    string    `json:"server"`
	Room      string    `json:"room"`
	Key       string    `json:"key"`
	ExpiresAt time.Time `json:"expiresAt"`
	Revoked   bool      `json:"revoked,omitempty"`
}

// NewPairing mints a fresh room and secret on the given relay.
func NewPairing(server string, now time.Time, ttl time.Duration) (Pairing, error) {
	server = NormalizeServer(server)
	if server == "" {
		return Pairing{}, errors.New("remote: empty server URL")
	}
	if ttl <= 0 || ttl > MaxRoomTTL {
		ttl = MaxRoomTTL
	}
	key := make([]byte, keyBytes)
	room := make([]byte, roomBytes)
	if _, err := rand.Read(key); err != nil {
		return Pairing{}, err
	}
	if _, err := rand.Read(room); err != nil {
		return Pairing{}, err
	}
	return Pairing{
		Server:    server,
		Room:      encoding.EncodeToString(room),
		Key:       encoding.EncodeToString(key),
		ExpiresAt: now.Add(ttl).UTC().Truncate(time.Second),
	}, nil
}

// NormalizeServer accepts what a user is likely to type and returns a base URL
// with no trailing slash. It does not invent a scheme: a bare host is ambiguous
// between http and https, and guessing wrong would silently send a pairing
// secret over the wrong transport.
func NormalizeServer(server string) string {
	server = strings.TrimSpace(server)
	if server == "" {
		return ""
	}
	return strings.TrimRight(server, "/")
}

// URL is what the QR code encodes: the room in the path and the secret in the
// fragment, which a browser never sends to a server.
func (p Pairing) URL() string {
	if p.Server == "" || p.Room == "" || p.Key == "" {
		return ""
	}
	return p.Server + "/p/" + p.Room + "#" + p.Key
}

// Live reports whether the pairing may still be used at now.
func (p Pairing) Live(now time.Time) bool {
	return !p.Revoked && p.Key != "" && p.Room != "" && now.Before(p.ExpiresAt)
}

// Secret returns the raw pairing secret.
func (p Pairing) Secret() ([]byte, error) {
	key, err := encoding.DecodeString(p.Key)
	if err != nil {
		return nil, fmt.Errorf("remote: pairing key is not base64url: %w", err)
	}
	if len(key) != keyBytes {
		return nil, fmt.Errorf("remote: pairing key is %d bytes, want %d", len(key), keyBytes)
	}
	return key, nil
}

// Proof is the bearer credential the relay stores and compares. It is a keyed
// hash of a fixed string: it proves possession of the secret without disclosing
// it, and it cannot be turned back into the secret.
func (p Pairing) Proof() (string, error) {
	key, err := p.Secret()
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(proofDomain))
	return encoding.EncodeToString(mac.Sum(nil)), nil
}

func (p Pairing) encryptKey() ([]byte, error) {
	return p.derive(kdfInfoEncrypt)
}

func (p Pairing) commandKey() ([]byte, error) {
	return p.derive(kdfInfoCommand)
}

func (p Pairing) derive(info string) ([]byte, error) {
	key, err := p.Secret()
	if err != nil {
		return nil, err
	}
	return hkdf.Key(sha256.New, key, []byte(kdfSalt), info, keyBytes)
}

// SealMessage encrypts one state or event payload. The additional data binds the
// ciphertext to its room, kind and sequence, so the relay cannot replay a state
// as an event or move a message between rooms.
func (p Pairing) SealMessage(kind string, seq int64, plaintext []byte) (iv, ct string, err error) {
	key, err := p.encryptKey()
	if err != nil {
		return "", "", err
	}
	return seal(key, messageAAD(p.Room, kind, seq), plaintext)
}

// OpenMessage decrypts and authenticates one state or event payload.
func (p Pairing) OpenMessage(kind string, seq int64, iv, ct string) ([]byte, error) {
	key, err := p.encryptKey()
	if err != nil {
		return nil, err
	}
	return open(key, messageAAD(p.Room, kind, seq), iv, ct)
}

// OpenCommand decrypts one command payload from the guest. The command MAC is
// verified separately by VerifyCommand.
func (p Pairing) OpenCommand(id, iv, ct string) ([]byte, error) {
	key, err := p.encryptKey()
	if err != nil {
		return nil, err
	}
	return open(key, commandAAD(p.Room, id), iv, ct)
}

// SealCommand encrypts one command. It exists for tests and for a future
// Duo-to-Duo client; the shipped guest is the page.
func (p Pairing) SealCommand(id string, plaintext []byte) (iv, ct string, err error) {
	key, err := p.encryptKey()
	if err != nil {
		return "", "", err
	}
	return seal(key, commandAAD(p.Room, id), plaintext)
}

// SignCommand authenticates the fields a guest may not let the relay change.
// The MAC covers the four fields directly rather than the serialized JSON, so
// both sides compute it without a canonical-JSON rule: id and kind cannot
// contain a separator, and n is decimal.
func (p Pairing) SignCommand(id, kind string, n int64, text string) (string, error) {
	key, err := p.commandKey()
	if err != nil {
		return "", err
	}
	return commandMAC(key, id, kind, n, text), nil
}

// VerifyCommand checks a command MAC in constant time.
func (p Pairing) VerifyCommand(id, kind string, n int64, text, mac string) error {
	want, err := p.SignCommand(id, kind, n, text)
	if err != nil {
		return err
	}
	if !constantTimeEqual(want, mac) {
		return errors.New("remote: command MAC does not verify")
	}
	return nil
}

func messageAAD(room, kind string, seq int64) []byte {
	return []byte(room + "|" + kind + "|" + strconv.FormatInt(seq, 10))
}

func commandAAD(room, id string) []byte {
	return []byte(room + "|cmd|" + id)
}

func commandMAC(key []byte, id, kind string, n int64, text string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id))
	mac.Write([]byte("|"))
	mac.Write([]byte(kind))
	mac.Write([]byte("|"))
	mac.Write([]byte(strconv.FormatInt(n, 10)))
	mac.Write([]byte("|"))
	mac.Write([]byte(text))
	return encoding.EncodeToString(mac.Sum(nil))
}

func seal(key, aad, plaintext []byte) (iv, ct string, err error) {
	aead, err := newAEAD(key)
	if err != nil {
		return "", "", err
	}
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return "", "", err
	}
	return encoding.EncodeToString(nonce),
		encoding.EncodeToString(aead.Seal(nil, nonce, plaintext, aad)), nil
}

func open(key, aad []byte, ivB64, ctB64 string) ([]byte, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	nonce, err := encoding.DecodeString(ivB64)
	if err != nil || len(nonce) != nonceSize {
		return nil, errors.New("remote: malformed IV")
	}
	ct, err := encoding.DecodeString(ctB64)
	if err != nil {
		return nil, errors.New("remote: malformed ciphertext")
	}
	plaintext, err := aead.Open(nil, nonce, ct, aad)
	if err != nil {
		return nil, errors.New("remote: message does not authenticate")
	}
	return plaintext, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// constantTimeEqual compares two strings without leaking where they differ.
// crypto/subtle is the right tool for the relay (which compares a stored proof
// against a claimed one) and for this client, which only compares a MAC.
func constantTimeEqual(a, b string) bool {
	return hmac.Equal([]byte(a), []byte(b))
}

// ValidProof reports whether a claimed proof is well-formed. Both sides check
// this before comparing, so a malformed value is rejected as auth rather than
// measured.
func ValidProof(proof string) bool {
	if len(proof) != proofLength {
		return false
	}
	raw, err := encoding.DecodeString(proof)
	return err == nil && len(raw) == sha256.Size
}

// ValidRoom reports whether a room id is well-formed.
func ValidRoom(room string) bool {
	if len(room) != roomLength {
		return false
	}
	raw, err := encoding.DecodeString(room)
	return err == nil && len(raw) == roomBytes
}

// RoomFromURL pulls the room id out of a scanned URL, ignoring the fragment.
func RoomFromURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if i := strings.IndexByte(raw, '#'); i >= 0 {
		raw = raw[:i]
	}
	if i := strings.Index(raw, "/p/"); i >= 0 {
		raw = raw[i+len("/p/"):]
	}
	raw = strings.Trim(raw, "/")
	if !ValidRoom(raw) {
		return ""
	}
	return raw
}
