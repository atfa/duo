package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Sink is what a guest may ask the local session to do. The caller implements it
// against the session seam, so this package never imports workflow state and a
// command can only reach the same bounded verbs a human has.
type Sink interface {
	SubmitTask(text string) error
	NewSession() error
	RequestObserverReport()
}

// Options configures a Client.
type Options struct {
	Pairing Pairing
	Sink    Sink
	// Snapshot returns the state to publish. It is called on every publish, on
	// connect and after every command, so a phone that reconnects always sees
	// current state rather than a stale one.
	Snapshot func() State
	// HTTP registers and revokes the room. Nil means a client with a 15 s timeout.
	HTTP *http.Client
	// Dialer is the WebSocket dialer. Nil means one that offers Subprotocol.
	Dialer *websocket.Dialer
	// Logf reports transport problems. Nil discards them: a relay that is down is
	// not a reason to interrupt the human, and the local session keeps working.
	Logf func(format string, args ...any)
	// Now is the clock, for tests.
	Now func() time.Time
	// RetryBase and RetryMax bound the reconnect backoff.
	RetryBase time.Duration
	RetryMax  time.Duration
}

// Status is the connection as a frontend may render it.
type Status struct {
	Active      bool      `json:"active"`
	Server      string    `json:"server"`
	Room        string    `json:"room"`
	ExpiresAt   time.Time `json:"expiresAt"`
	Connected   bool      `json:"connected"`
	LastAck     int64     `json:"lastAck"`
	LastError   string    `json:"lastError,omitempty"`
	Published   int64     `json:"published"`
	HostPresent bool      `json:"hostPresent"`
	Guests      int       `json:"guests"`
}

// Client keeps one room connected and republishes state as it changes.
//
// It is deliberately small: a single read loop, a coalesced state writer and a
// keepalive. A relay that refuses, expires or throttles the room only closes
// this connection; nothing here can stop the local session.
type Client struct {
	opts Options

	mu     sync.Mutex
	status Status
	conn   *websocket.Conn

	writeMu sync.Mutex
	sendMu  sync.Mutex

	seq    int64
	nonce  int64
	nonces []int64
	seen   map[string]bool
	last   *CommandResult

	dirty chan struct{}

	cancel   context.CancelFunc
	done     chan struct{}
	stopOnce sync.Once
}

// New validates the options and returns a client that is not connected yet.
func New(opts Options) (*Client, error) {
	if _, err := opts.Pairing.Proof(); err != nil {
		return nil, err
	}
	if !ValidRoom(opts.Pairing.Room) {
		return nil, errors.New("remote: malformed room id")
	}
	if opts.Sink == nil {
		return nil, errors.New("remote: no command sink")
	}
	if opts.Snapshot == nil {
		return nil, errors.New("remote: no snapshot source")
	}
	if opts.HTTP == nil {
		opts.HTTP = &http.Client{Timeout: 15 * time.Second}
	}
	if opts.Dialer == nil {
		opts.Dialer = &websocket.Dialer{Subprotocols: []string{Subprotocol}, HandshakeTimeout: 15 * time.Second}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.RetryBase <= 0 {
		opts.RetryBase = time.Second
	}
	if opts.RetryMax <= 0 {
		opts.RetryMax = 30 * time.Second
	}
	c := &Client{
		opts:  opts,
		dirty: make(chan struct{}, 1),
		done:  make(chan struct{}),
		seen:  make(map[string]bool),
	}
	// A fresh nonce base per process: a command replayed from a previous process
	// must not match the window of this one.
	c.nonce = rand.Int64N(1 << 40)
	c.status = Status{
		Active:    true,
		Server:    opts.Pairing.Server,
		Room:      opts.Pairing.Room,
		ExpiresAt: opts.Pairing.ExpiresAt,
	}
	return c, nil
}

// Run keeps the room connected until ctx is cancelled or the relay ends the room
// for good. It never returns an error: a relay that is unreachable is a
// transient condition, and the local session is unaffected either way.
func (c *Client) Run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.cancel = cancel
	c.mu.Unlock()
	defer close(c.done)

	backoff := c.opts.RetryBase
	for {
		if ctx.Err() != nil {
			return
		}
		err := c.connectAndServe(ctx)
		if ctx.Err() != nil {
			return
		}
		var terminal *ErrTerminal
		if errors.As(err, &terminal) {
			c.setActive(false)
			c.setError(terminal.Error())
			// A room the relay ended for good is the one failure nothing else
			// reports: the socket stops, and the interface only knows the pairing
			// is no longer live. The session log has to carry the code, or the
			// phone's "offline" has no explanation anywhere on the computer.
			c.logf("stopped: %v", terminal)
			return
		}
		if err != nil {
			c.setError(err.Error())
			c.logf("%v", err)
		}
		backoff = c.wait(ctx, backoff)
	}
}

// Register creates or refreshes the room on the relay. Callers that expose the
// pairing to a user should wait for this to succeed before showing its QR code.
// Run registers again before each reconnect, making recovery idempotent.
func (c *Client) RegisterRoom(ctx context.Context) error {
	return c.register(ctx)
}

// Close stops the client and its connection. It is safe to call more than once
// and before Run.
func (c *Client) Close() {
	c.stopOnce.Do(func() {
		c.mu.Lock()
		cancel := c.cancel
		conn := c.conn
		c.conn = nil
		c.status.Connected = false
		c.status.Active = false
		c.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		if conn != nil {
			_ = conn.Close()
		}
	})
}

// PublishState asks for a state publish. Requests coalesce: a burst of changes
// produces one frame, and the writer caps the rate at one per second.
//
// Nobody to send to means nothing to send: every frame costs the room's daily
// relay budget, and a session with no phone attached would spend it on a
// conversation nobody is reading. The state is published when a phone joins
// instead (see the presence handling), so a later guest still starts from a
// current frame.
func (c *Client) PublishState() {
	c.mu.Lock()
	guests := c.status.Guests
	c.mu.Unlock()
	if guests == 0 {
		return
	}
	select {
	case c.dirty <- struct{}{}:
	default:
	}
}

// Status returns the current connection projection.
func (c *Client) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

func (c *Client) connectAndServe(ctx context.Context) error {
	now := c.opts.Now()
	if c.opts.Pairing.Revoked {
		return &ErrTerminal{Code: CodeRevoked}
	}
	if !now.Before(c.opts.Pairing.ExpiresAt) {
		return &ErrTerminal{Code: CodeExpired}
	}
	if err := c.register(ctx); err != nil {
		return err
	}
	wsURL, err := webSocketURL(c.opts.Pairing.Server, c.opts.Pairing.Room)
	if err != nil {
		return err
	}
	conn, resp, err := c.opts.Dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusConflict {
			return &ErrTerminal{Code: CodeRoomFull}
		}
		return fmt.Errorf("remote: dial: %w", err)
	}
	defer conn.Close()
	if got := conn.Subprotocol(); got != Subprotocol {
		conn.Close()
		return fmt.Errorf("remote: relay selected subprotocol %q, want %q", got, Subprotocol)
	}
	c.attach(conn)
	defer c.detach(conn)

	proof, err := c.opts.Pairing.Proof()
	if err != nil {
		return err
	}
	c.mu.Lock()
	since := c.seq
	c.mu.Unlock()
	if err := c.write(frame{
		T: frameHello, V: ProtocolVersion, Role: RoleHost,
		Room: c.opts.Pairing.Room, Proof: proof, Since: since,
	}); err != nil {
		return err
	}
	_ = conn.SetReadDeadline(c.opts.Now().Add(20 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("remote: waiting for ready: %w", err)
	}
	ready, err := decodeFrame(data)
	if err != nil {
		return err
	}
	switch ready.T {
	case frameError:
		return &ErrTerminal{Code: ready.Code}
	case frameReady:
	default:
		return fmt.Errorf("remote: relay answered %q, want ready", ready.T)
	}
	if ready.Room != "" && ready.Room != c.opts.Pairing.Room {
		return errors.New("remote: relay answered for a different room")
	}
	if ready.ExpiresAt > 0 {
		expires := time.Unix(ready.ExpiresAt, 0)
		c.mu.Lock()
		c.status.ExpiresAt = expires
		c.mu.Unlock()
	}
	c.mu.Lock()
	if ready.Seq > c.seq {
		c.seq = ready.Seq
	}
	c.status.Connected = true
	c.status.LastError = ""
	c.mu.Unlock()
	c.logf("connected to room %s", c.opts.Pairing.Room)

	if err := c.sendState(ctx); err != nil {
		return err
	}
	_ = conn.SetReadDeadline(c.opts.Now().Add(readIdleTimeout))

	done := make(chan error, 3)
	go c.keepalive(ctx, conn, done)
	go c.pushStates(ctx, conn, done)
	go c.readLoop(ctx, conn, done)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}

const (
	// keepalivePeriod is deliberately slow: a hibernating Durable Object costs
	// nothing, so there is no reason to ping faster than the room's idle budget.
	keepalivePeriod = 30 * time.Second
	// readIdleTimeout drops a socket whose keepalive stopped being answered.
	readIdleTimeout = 90 * time.Second
	// pushInterval coalesces state writes to at most one per second. The relay
	// charges every host frame to the room's daily budget, and a phone cannot
	// read faster than this anyway.
	pushInterval = time.Second
)

func (c *Client) keepalive(ctx context.Context, conn *websocket.Conn, done chan<- error) {
	ticker := time.NewTicker(keepalivePeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.write(frame{T: framePing}); err != nil {
				done <- err
				return
			}
			// The read deadline is the real liveness check: a relay that stops
			// answering pings fails the next read and the client reconnects.
			_ = conn.SetReadDeadline(c.opts.Now().Add(readIdleTimeout))
		}
	}
}

func (c *Client) pushStates(ctx context.Context, conn *websocket.Conn, done chan<- error) {
	ticker := time.NewTicker(pushInterval)
	defer ticker.Stop()
	pending := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.dirty:
			pending = true
		case <-ticker.C:
			if !pending {
				continue
			}
			pending = false
			if err := c.sendState(ctx); err != nil {
				done <- err
				return
			}
		}
	}
}

func (c *Client) readLoop(ctx context.Context, conn *websocket.Conn, done chan<- error) {
	for {
		if ctx.Err() != nil {
			return
		}
		_, data, err := conn.ReadMessage()
		if err != nil {
			done <- err
			return
		}
		f, err := decodeFrame(data)
		if err != nil {
			c.logf("relay sent an unreadable frame: %v", err)
			continue
		}
		switch f.T {
		case frameCmd:
			c.handleCommand(f)
		case framePresence:
			c.mu.Lock()
			joined := f.Guests > c.status.Guests
			c.status.HostPresent = f.Host
			c.status.Guests = f.Guests
			c.mu.Unlock()
			if joined {
				// A phone just arrived. It may have joined after the last change, so
				// the frame it gets is the current state rather than whatever the
				// relay happened to store.
				c.PublishState()
			}
		case frameAck:
			c.mu.Lock()
			if f.Seq > c.status.LastAck {
				c.status.LastAck = f.Seq
			}
			c.mu.Unlock()
		case framePing:
			if err := c.write(frame{T: framePong}); err != nil {
				done <- err
				return
			}
		case framePong:
			_ = conn.SetReadDeadline(c.opts.Now().Add(readIdleTimeout))
		case frameBye:
			done <- &ErrTerminal{Code: f.Code}
			return
		case frameError:
			if f.Code == CodeAuth || f.Code == CodeExpired || f.Code == CodeRoomFull {
				done <- &ErrTerminal{Code: f.Code}
				return
			}
			c.logf("relay refused a frame: %s", f.Code)
		}
	}
}

func (c *Client) handleCommand(f frame) {
	plaintext, err := c.opts.Pairing.OpenCommand(f.ID, f.IV, f.CT)
	if err == nil {
		err = c.applyCommand(f, plaintext)
	}
	if err != nil {
		c.logf("dropped a command: %v", err)
	}
	c.PublishState()
}

// applyCommand authenticates, deduplicates and runs one command. The returned
// error is the answer the phone sees, not only a local log line: a command that
// the session refused has to be visible on the phone, which is the only place
// the human is looking.
func (c *Client) applyCommand(f frame, plaintext []byte) error {
	var cmd Command
	if err := json.Unmarshal(plaintext, &cmd); err != nil {
		return fmt.Errorf("malformed command: %w", err)
	}
	if cmd.ID != f.ID {
		return errors.New("command id does not match its envelope")
	}
	if err := c.opts.Pairing.VerifyCommand(cmd.ID, cmd.Kind, cmd.N, cmd.Text, cmd.MAC); err != nil {
		return errors.New("the command did not authenticate")
	}
	c.record(&CommandResult{ID: cmd.ID, Kind: cmd.Kind, Accepted: true})
	if !c.nonceValid(cmd.N) {
		return c.reject(cmd.ID, cmd.Kind, "the command was for an older state; send it again")
	}
	if c.markSeen(cmd.ID) {
		// A repeated command is a retry after a lost ack, not a second action.
		return c.reject(cmd.ID, cmd.Kind, "the command was already executed")
	}
	switch cmd.Kind {
	case CmdTask:
		text := strings.TrimSpace(cmd.Text)
		if text == "" {
			return c.reject(cmd.ID, cmd.Kind, "the task was empty")
		}
		if err := c.opts.Sink.SubmitTask(text); err != nil {
			return c.reject(cmd.ID, cmd.Kind, err.Error())
		}
	case CmdNewSession:
		if err := c.opts.Sink.NewSession(); err != nil {
			return c.reject(cmd.ID, cmd.Kind, err.Error())
		}
	case CmdObserverReport:
		c.opts.Sink.RequestObserverReport()
	default:
		return c.reject(cmd.ID, cmd.Kind, fmt.Sprintf("unsupported command %q", cmd.Kind))
	}
	return nil
}

// reject records a refused command and returns the reason, so every refusal
// path reports the same way.
func (c *Client) reject(id, kind, note string) error {
	c.record(&CommandResult{ID: id, Kind: kind, Accepted: false, Note: note})
	return errors.New(note)
}

func (c *Client) record(result *CommandResult) {
	c.mu.Lock()
	c.last = result
	c.mu.Unlock()
}

func (c *Client) lastResult() *CommandResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last == nil {
		return nil
	}
	copy := *c.last
	return &copy
}

func (c *Client) sendState(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	state := c.opts.Snapshot()
	state.V = ProtocolVersion
	state.N = c.issueNonce()
	state.TS = c.opts.Now().UnixMilli()
	state.Command = c.lastResult()
	plaintext, err := fitState(state)
	if err != nil {
		return err
	}
	return c.sendMessage(KindState, plaintext)
}

// PublishEvent sends one incremental update. A caller that cannot describe a
// change as an event publishes a state instead, so the phone always converges.
// Like PublishState it sends nothing while no phone is attached: a joining
// phone is given a current state, which already carries recent activity.
func (c *Client) PublishEvent(ev Event) error {
	c.mu.Lock()
	guests := c.status.Guests
	c.mu.Unlock()
	if guests == 0 {
		return nil
	}
	ev.V = ProtocolVersion
	ev.N = c.issueNonce()
	if ev.At == 0 {
		ev.At = c.opts.Now().UnixMilli()
	}
	plaintext, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if len(plaintext) > MaxPlaintext {
		return errors.New("remote: event payload is too large")
	}
	return c.sendMessage(KindEvent, plaintext)
}

func (c *Client) sendMessage(kind string, plaintext []byte) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	c.mu.Lock()
	seq := c.seq + 1
	c.mu.Unlock()
	iv, ct, err := c.opts.Pairing.SealMessage(kind, seq, plaintext)
	if err != nil {
		return err
	}
	if err := c.write(frame{T: frameMsg, Seq: seq, Kind: kind, IV: iv, CT: ct}); err != nil {
		return err
	}
	c.mu.Lock()
	c.seq = seq
	c.status.Published++
	c.mu.Unlock()
	return nil
}

func (c *Client) write(f frame) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return errors.New("remote: not connected")
	}
	data, err := f.encode()
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = conn.SetWriteDeadline(c.opts.Now().Add(10 * time.Second))
	return conn.WriteMessage(websocket.TextMessage, data)
}

func (c *Client) register(ctx context.Context) error {
	proof, err := c.opts.Pairing.Proof()
	if err != nil {
		return err
	}
	ttl := int64(c.opts.Pairing.ExpiresAt.Sub(c.opts.Now()).Seconds())
	if ttl <= 0 {
		return &ErrTerminal{Code: CodeExpired}
	}
	if ttl > int64(MaxRoomTTL.Seconds()) {
		ttl = int64(MaxRoomTTL.Seconds())
	}
	body, err := json.Marshal(map[string]any{
		"proof":      proof,
		"ttlSeconds": ttl,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(c.opts.Pairing.Server, "/")+"/api/room?room="+url.QueryEscape(c.opts.Pairing.Room),
		bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.opts.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("remote: register: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden ||
		resp.StatusCode == http.StatusConflict:
		return &ErrTerminal{Code: CodeAuth}
	case resp.StatusCode == http.StatusTooManyRequests:
		return fmt.Errorf("remote: registration rate limited")
	default:
		return fmt.Errorf("remote: register: %s", resp.Status)
	}
}

// Revoke deletes the room on the relay. It is the /remote off path: the caller
// closes the client and deletes the pairing file in the same action.
func (c *Client) Revoke(ctx context.Context) error {
	proof, err := c.opts.Pairing.Proof()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		strings.TrimRight(c.opts.Pairing.Server, "/")+"/api/room/"+url.PathEscape(c.opts.Pairing.Room), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+proof)
	resp, err := c.opts.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("remote: revoke: %s", resp.Status)
	}
	return nil
}

func (c *Client) issueNonce() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nonce++
	if c.nonce <= 0 {
		c.nonce = 1
	}
	c.nonces = append(c.nonces, c.nonce)
	if len(c.nonces) > 8 {
		c.nonces = c.nonces[len(c.nonces)-8:]
	}
	return c.nonce
}

func (c *Client) nonceValid(n int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, issued := range c.nonces {
		if issued == n {
			return true
		}
	}
	return false
}

// markSeen records a command id and reports whether it had already been seen.
func (c *Client) markSeen(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seen[id] {
		return true
	}
	if len(c.seen) > 256 {
		// Bounded memory on a long session: the ids that matter are the recent
		// ones, and an ancient retry would fail the nonce window anyway.
		c.seen = make(map[string]bool, 256)
	}
	c.seen[id] = true
	return false
}

func (c *Client) attach(conn *websocket.Conn) {
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
}

func (c *Client) detach(conn *websocket.Conn) {
	c.mu.Lock()
	if c.conn == conn {
		c.conn = nil
		c.status.Connected = false
	}
	c.mu.Unlock()
}

func (c *Client) setError(msg string) {
	c.mu.Lock()
	c.status.LastError = msg
	c.mu.Unlock()
}

func (c *Client) setActive(active bool) {
	c.mu.Lock()
	c.status.Active = active
	if !active {
		c.status.Connected = false
	}
	c.mu.Unlock()
}

func (c *Client) logf(format string, args ...any) {
	if c.opts.Logf != nil {
		c.opts.Logf(format, args...)
	}
}

// wait sleeps for the backoff with jitter and returns the next one.
func (c *Client) wait(ctx context.Context, backoff time.Duration) time.Duration {
	delay := backoff/2 + rand.N(backoff/2+1)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
	next := backoff * 2
	if next > c.opts.RetryMax {
		next = c.opts.RetryMax
	}
	return next
}

// fitState trims the variable-length parts of a snapshot until it fits one
// frame. Recent activity goes first from the oldest end, then reports, then the
// plan line: the current state is more useful to a phone than history.
func fitState(state State) ([]byte, error) {
	for {
		data, err := json.Marshal(state)
		if err != nil {
			return nil, err
		}
		if len(data) <= MaxPlaintext {
			return data, nil
		}
		switch {
		case len(state.Activity) > 0:
			state.Activity = state.Activity[1:]
		case len(state.Observer.Reports) > 0:
			state.Observer.Reports = state.Observer.Reports[1:]
		case len(state.Plan) > 200:
			state.Plan = truncate(state.Plan, len(state.Plan)/2)
		case len(state.Activity) == 0 && state.Plan != "":
			state.Plan = ""
		default:
			return nil, errors.New("remote: state payload is too large")
		}
	}
}

// webSocketURL turns the relay base URL into its WebSocket endpoint. A plain
// http base is only accepted for a local relay: sending a pairing secret over
// cleartext to a remote host is exactly the mistake this check prevents.
func webSocketURL(server, room string) (string, error) {
	u, err := url.Parse(NormalizeServer(server))
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		if !isLoopback(u.Hostname()) {
			return "", errors.New("remote: refusing a cleartext relay for a non-local host")
		}
		u.Scheme = "ws"
	case "wss", "ws":
	default:
		return "", fmt.Errorf("remote: unsupported relay scheme %q", u.Scheme)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/ws"
	q := u.Query()
	q.Set("room", room)
	u.RawQuery = q.Encode()
	u.Fragment = ""
	return u.String(), nil
}

func isLoopback(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return strings.HasSuffix(host, ".localhost")
}
