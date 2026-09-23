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
	Agent protocol.AgentID
	conn  net.Conn
	mu    sync.Mutex
}

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
