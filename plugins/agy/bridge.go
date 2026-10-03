package agy

import (
	"context"
	"sync"
	"time"

	"github.com/atfa/duo/internal/mcp"
	"github.com/atfa/duo/internal/protocol"
)

// bridgeSink reports watcher messages to Duo Core over the bridge. It is the
// Agent Adapter's whole transport: the plugin process dials the endpoint Core
// announced in the launch environment, so Core never learns that agy is
// observed at all — from its side the only difference is `activity: true`.
//
// The connection is made on the first message and re-made after a failure, so a
// session whose bridge is not up yet starts reporting the moment it is, and one
// dropped socket costs the next message rather than the session. It is also
// the agent's single endpoint (bridge: plugin): this process connects, and
// nothing inside agy may, or the two would evict each other.
type bridgeSink struct {
	cfg mcp.Config

	mu sync.Mutex
	bc *mcp.BridgeClient
}

// newBridgeSink builds a sink for one agent's endpoint. The identity fields
// come from the launch environment, which is also what lets the sink be reused
// across agent restarts without a new handshake upstream can observe.
func newBridgeSink(agent protocol.AgentID, session, token, host, port string) *bridgeSink {
	return &bridgeSink{cfg: mcp.Config{
		Agent:      agent,
		SessionID:  session,
		Token:      token,
		Host:       host,
		Port:       port,
		ClientType: "plugin",
	}}
}

// OnActivity implements AgyActivitySink. A message that cannot be delivered is
// dropped and the connection retired: the watcher must never block on Core,
// and the next observation reconnects.
func (s *bridgeSink) OnActivity(_ protocol.AgentID, msg protocol.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bc == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		bc, err := mcp.ConnectBridge(ctx, s.cfg)
		cancel()
		if err != nil {
			return
		}
		s.bc = bc
	}
	if err := s.bc.Send(msg); err != nil {
		_ = s.bc.Close()
		s.bc = nil
	}
}

// Close releases the endpoint. Called when the driver closes, so an in-process
// driver does not hold a socket for the lifetime of the process.
func (s *bridgeSink) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bc != nil {
		_ = s.bc.Close()
		s.bc = nil
	}
}
