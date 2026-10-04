package transport

import (
	"context"
	"encoding/json"
	"net"
	"sync"
	"time"

	"github.com/atfa/duo/internal/protocol"
)

type Client struct {
	agent      protocol.AgentID
	clientType string
	conn       net.Conn
	mu         sync.Mutex
}

// Identity is the agent this client authenticated as.
//
// It is set exactly once, by the transport's validated MsgHello handshake, and it
// is deliberately not an exported field: there is no constructor and no setter
// outside this package, so no code outside transport can fabricate an agent's
// identity and speak with its authority. Only an agent's live, authenticated
// socket can produce a Client at all.
func (c *Client) Identity() protocol.AgentID { return c.agent }

func newClient(conn net.Conn) *Client {
	return &Client{conn: conn}
}

func (c *Client) Send(ctx context.Context, message protocol.Message) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	deadline := time.Now().Add(5 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	defer c.conn.SetWriteDeadline(time.Time{})

	_, err = c.conn.Write(data)
	return err
}

func (c *Client) Close() error {
	return c.conn.Close()
}
