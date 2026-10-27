package remote

import (
	"crypto/hmac"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeRelay is an in-process implementation of the documented relay: room
// registration, revocation, host takeover, replay from `since`, command dedupe
// and the handshake rules. It is deliberately strict, so the client is tested
// against the contract rather than against a lenient stub.
type fakeRelay struct {
	t     *testing.T
	srv   *httptest.Server
	mu    sync.Mutex
	rooms map[string]*fakeRoom
}

type fakeRoom struct {
	proof     string
	expiresAt time.Time
	host      *fakePeer
	guests    map[*fakePeer]bool
	msgs      []frame
	cmds      map[string]bool
	pings     int
}

type fakePeer struct {
	relay   *fakeRelay
	room    string
	role    string
	conn    *websocket.Conn
	writeMu sync.Mutex
	lastSeq int64
}

func newFakeRelay(t *testing.T) *fakeRelay {
	t.Helper()
	r := &fakeRelay{t: t, rooms: make(map[string]*fakeRoom)}
	r.srv = httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *fakeRelay) url() string { return r.srv.URL }

func (r *fakeRelay) serve(w http.ResponseWriter, req *http.Request) {
	switch {
	case req.Method == http.MethodPost && req.URL.Path == "/api/room":
		r.register(w, req)
	case req.Method == http.MethodDelete && strings.HasPrefix(req.URL.Path, "/api/room/"):
		r.revoke(w, req)
	case req.URL.Path == "/ws":
		r.upgrade(w, req)
	default:
		http.NotFound(w, req)
	}
}

func (r *fakeRelay) register(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Room       string `json:"room"`
		Proof      string `json:"proof"`
		TTLSeconds int64  `json:"ttlSeconds"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// The room travels in the query string; the body carries the proof and TTL.
	if room := req.URL.Query().Get("room"); room != "" {
		body.Room = room
	}
	if !ValidRoom(body.Room) || !ValidProof(body.Proof) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ttl := time.Duration(body.TTLSeconds) * time.Second
	if ttl <= 0 || ttl > MaxRoomTTL {
		ttl = MaxRoomTTL
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if room, ok := r.rooms[body.Room]; ok {
		if !hmac.Equal([]byte(room.proof), []byte(body.Proof)) {
			http.Error(w, "room taken", http.StatusConflict)
			return
		}
		room.expiresAt = time.Now().Add(ttl)
		w.WriteHeader(http.StatusOK)
		return
	}
	r.rooms[body.Room] = &fakeRoom{
		proof:     body.Proof,
		expiresAt: time.Now().Add(ttl),
		guests:    make(map[*fakePeer]bool),
		cmds:      make(map[string]bool),
	}
	w.WriteHeader(http.StatusCreated)
}

func (r *fakeRelay) revoke(w http.ResponseWriter, req *http.Request) {
	room := strings.TrimPrefix(req.URL.Path, "/api/room/")
	proof := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
	r.mu.Lock()
	stored, ok := r.rooms[room]
	if !ok {
		r.mu.Unlock()
		http.NotFound(w, req)
		return
	}
	if !hmac.Equal([]byte(stored.proof), []byte(proof)) {
		r.mu.Unlock()
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	delete(r.rooms, room)
	peers := stored.peers()
	r.mu.Unlock()
	for _, p := range peers {
		p.send(frame{T: frameBye, Code: CodeRevoked})
		_ = p.conn.Close()
	}
	w.WriteHeader(http.StatusNoContent)
}

func (room *fakeRoom) peers() []*fakePeer {
	out := make([]*fakePeer, 0, len(room.guests)+1)
	if room.host != nil {
		out = append(out, room.host)
	}
	for g := range room.guests {
		out = append(out, g)
	}
	return out
}

func (r *fakeRelay) upgrade(w http.ResponseWriter, req *http.Request) {
	room := req.URL.Query().Get("room")
	if !ValidRoom(room) {
		http.Error(w, "bad room", http.StatusBadRequest)
		return
	}
	up := websocket.Upgrader{Subprotocols: []string{Subprotocol}}
	conn, err := up.Upgrade(w, req, nil)
	if err != nil {
		return
	}
	peer := &fakePeer{relay: r, room: room, conn: conn}
	go r.servePeer(peer)
}

func (r *fakeRelay) servePeer(peer *fakePeer) {
	defer func() {
		r.mu.Lock()
		if room, ok := r.rooms[peer.room]; ok {
			if room.host == peer {
				room.host = nil
			}
			delete(room.guests, peer)
		}
		r.mu.Unlock()
		_ = peer.conn.Close()
		r.sendPresence(peer.room)
	}()

	_ = peer.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := peer.conn.ReadMessage()
	if err != nil {
		return
	}
	hello, err := decodeFrame(data)
	if err != nil || hello.T != frameHello {
		peer.send(frame{T: frameError, Code: CodeAuth})
		return
	}
	if hello.Room != peer.room || (hello.Role != RoleHost && hello.Role != RoleGuest) || !ValidProof(hello.Proof) {
		peer.send(frame{T: frameError, Code: CodeAuth})
		return
	}
	r.mu.Lock()
	room, ok := r.rooms[peer.room]
	if !ok {
		r.mu.Unlock()
		peer.send(frame{T: frameError, Code: CodeAuth})
		return
	}
	if !hmac.Equal([]byte(room.proof), []byte(hello.Proof)) {
		r.mu.Unlock()
		peer.send(frame{T: frameError, Code: CodeAuth})
		return
	}
	if time.Now().After(room.expiresAt) {
		r.mu.Unlock()
		peer.send(frame{T: frameError, Code: CodeExpired})
		return
	}
	peer.role = hello.Role
	var replaced *fakePeer
	if hello.Role == RoleHost {
		replaced = room.host
		room.host = peer
	} else {
		room.guests[peer] = true
	}
	replay := make([]frame, 0, len(room.msgs))
	for _, m := range room.msgs {
		if m.Seq > hello.Since {
			replay = append(replay, m)
		}
	}
	latest := int64(0)
	if len(room.msgs) > 0 {
		latest = room.msgs[len(room.msgs)-1].Seq
	}
	r.mu.Unlock()

	if replaced != nil {
		replaced.send(frame{T: frameBye, Code: CodeReplaced})
		_ = replaced.conn.Close()
	}
	peer.send(frame{T: frameReady, V: ProtocolVersion, Room: peer.room, Seq: latest, ExpiresAt: room.expiresAt.Unix()})
	for _, m := range replay {
		peer.send(m)
	}
	r.sendPresence(peer.room)

	_ = peer.conn.SetReadDeadline(time.Time{})
	for {
		_, data, err := peer.conn.ReadMessage()
		if err != nil {
			return
		}
		f, err := decodeFrame(data)
		if err != nil {
			peer.send(frame{T: frameError, Code: CodeBadFrame})
			return
		}
		switch f.T {
		case frameMsg:
			if peer.role != RoleHost {
				peer.send(frame{T: frameError, Code: CodeAuth})
				return
			}
			r.mu.Lock()
			room := r.rooms[peer.room]
			if room == nil || room.host != peer {
				r.mu.Unlock()
				return
			}
			if len(room.msgs) > 0 && f.Seq <= room.msgs[len(room.msgs)-1].Seq {
				r.mu.Unlock()
				peer.send(frame{T: frameError, Code: CodeBadFrame})
				return
			}
			room.msgs = append(room.msgs, f)
			if len(room.msgs) > 50 {
				room.msgs = room.msgs[len(room.msgs)-50:]
			}
			guests := make([]*fakePeer, 0, len(room.guests))
			for g := range room.guests {
				guests = append(guests, g)
			}
			r.mu.Unlock()
			for _, g := range guests {
				g.send(f)
			}
		case frameCmd:
			if peer.role != RoleGuest {
				peer.send(frame{T: frameError, Code: CodeAuth})
				return
			}
			r.mu.Lock()
			room := r.rooms[peer.room]
			if room == nil {
				r.mu.Unlock()
				return
			}
			if room.cmds[f.ID] {
				r.mu.Unlock()
				continue
			}
			room.cmds[f.ID] = true
			host := room.host
			r.mu.Unlock()
			if host != nil {
				host.send(f)
			}
		case frameAck, framePing:
			if f.T == framePing {
				r.mu.Lock()
				if room := r.rooms[peer.room]; room != nil {
					room.pings++
				}
				r.mu.Unlock()
				peer.send(frame{T: framePong})
			}
		default:
			peer.send(frame{T: frameError, Code: CodeBadFrame})
			return
		}
	}
}

func (p *fakePeer) send(f frame) {
	data, err := f.encode()
	if err != nil {
		return
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_ = p.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_ = p.conn.WriteMessage(websocket.TextMessage, data)
}

// read returns the next frame, failing the test on timeout.
func (p *fakePeer) read(t *testing.T, timeout time.Duration) frame {
	t.Helper()
	_ = p.conn.SetReadDeadline(time.Now().Add(timeout))
	_, data, err := p.conn.ReadMessage()
	if err != nil {
		t.Fatalf("peer %s read: %v", p.role, err)
	}
	f, err := decodeFrame(data)
	if err != nil {
		t.Fatalf("peer %s frame: %v", p.role, err)
	}
	return f
}

// nextState waits for the next state message and decrypts it.
func (p *fakePeer) nextState(t *testing.T, pairing Pairing, timeout time.Duration) State {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		f := p.read(t, time.Until(deadline))
		if f.T != frameMsg || f.Kind != KindState {
			continue
		}
		plaintext, err := pairing.OpenMessage(f.Kind, f.Seq, f.IV, f.CT)
		if err != nil {
			t.Fatalf("guest could not open state: %v", err)
		}
		var state State
		if err := json.Unmarshal(plaintext, &state); err != nil {
			t.Fatalf("guest could not parse state: %v", err)
		}
		p.lastSeq = f.Seq
		return state
	}
}

func (r *fakeRelay) guest(t *testing.T, pairing Pairing) *fakePeer {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(r.url(), "http") + "/ws?room=" + pairing.Room
	dialer := &websocket.Dialer{Subprotocols: []string{Subprotocol}, HandshakeTimeout: 5 * time.Second}
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("guest dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	peer := &fakePeer{relay: r, room: pairing.Room, role: RoleGuest, conn: conn}
	proof, err := pairing.Proof()
	if err != nil {
		t.Fatalf("proof: %v", err)
	}
	peer.send(frame{T: frameHello, V: ProtocolVersion, Role: RoleGuest, Room: pairing.Room, Proof: proof})
	ready := peer.read(t, 5*time.Second)
	if ready.T != frameReady {
		t.Fatalf("guest handshake: got %+v", ready)
	}
	return peer
}

// nextMessage returns the next envelope of the given kind and decrypts it.
func (p *fakePeer) nextMessage(t *testing.T, pairing Pairing, kind string, timeout time.Duration) []byte {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		f := p.read(t, time.Until(deadline))
		if f.T != frameMsg || f.Kind != kind {
			continue
		}
		plaintext, err := pairing.OpenMessage(f.Kind, f.Seq, f.IV, f.CT)
		if err != nil {
			t.Fatalf("guest could not open %s: %v", kind, err)
		}
		p.lastSeq = f.Seq
		return plaintext
	}
}

// published reports how many envelopes the relay has stored for a room, which is
// what the daily message budget is charged for.
func (r *fakeRelay) published(roomID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.rooms[roomID]
	if !ok {
		return 0
	}
	return len(stored.msgs)
}

// pings counts the keepalives a room's host has sent, which is the only
// observable evidence that the interval reached the ticker.
func (r *fakeRelay) pings(roomID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.rooms[roomID]
	if !ok {
		return 0
	}
	return stored.pings
}

// sendPresence mirrors the relay's own frame. The page reads it to know whether
// the computer is reachable, and the host reads it to know whether a phone is
// attached before it spends the room's message budget.
func (r *fakeRelay) sendPresence(roomID string) {
	r.mu.Lock()
	stored, ok := r.rooms[roomID]
	if !ok {
		r.mu.Unlock()
		return
	}
	peers := stored.peers()
	frame := frame{T: framePresence, Host: stored.host != nil, Guests: len(stored.guests)}
	r.mu.Unlock()
	for _, peer := range peers {
		peer.send(frame)
	}
}

// endRoom is the relay ending a room for good, which is what an exhausted daily
// budget looks like from the client: every socket is told why and closed.
func (r *fakeRelay) endRoom(t *testing.T, pairing Pairing, code string) {
	t.Helper()
	r.mu.Lock()
	stored, ok := r.rooms[pairing.Room]
	var peers []*fakePeer
	if ok {
		peers = stored.peers()
	}
	r.mu.Unlock()
	if !ok {
		t.Fatalf("room %s is not registered", pairing.Room)
	}
	for _, peer := range peers {
		peer.send(frame{T: frameBye, Code: code})
		_ = peer.conn.Close()
	}
}

func (r *fakeRelay) dropHost(t *testing.T, room string) {
	t.Helper()
	r.mu.Lock()
	stored, ok := r.rooms[room]
	var host *fakePeer
	if ok {
		host = stored.host
		stored.host = nil
	}
	r.mu.Unlock()
	if host != nil {
		_ = host.conn.Close()
	}
}

func (r *fakeRelay) revokeRoom(t *testing.T, pairing Pairing) {
	t.Helper()
	proof, err := pairing.Proof()
	if err != nil {
		t.Fatalf("proof: %v", err)
	}
	req, err := http.NewRequest(http.MethodDelete, r.url()+"/api/room/"+pairing.Room, nil)
	if err != nil {
		t.Fatalf("revoke request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+proof)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke: %s", resp.Status)
	}
}
