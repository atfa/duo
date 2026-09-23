package transport

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"sync"

	"github.com/atfa/duo/internal/protocol"
)

type Handler interface {
	OnConnect(context.Context, *Client)
	OnDisconnect(*Client)
	OnMessage(context.Context, *Client, protocol.Message)
}

type Server struct {
	addr string

	mu      sync.RWMutex
	clients map[protocol.AgentID]*Client
	handler Handler

	listener net.Listener
}

func NewServer(addr string) *Server {
	return &Server{
		addr:    addr,
		clients: make(map[protocol.AgentID]*Client),
	}
}

func (s *Server) SetHandler(handler Handler) {
	s.handler = handler
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	s.listener = listener

	go func() {
		<-ctx.Done()
		_ = listener.Close()
		s.CloseAll()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go s.handleConnection(ctx, conn)
	}
}

func (s *Server) handleConnection(ctx context.Context, conn net.Conn) {
	client := newClient(conn)

	defer func() {
		s.unregister(client)
		_ = client.Close()
		if s.handler != nil && client.Agent != "" {
			s.handler.OnDisconnect(client)
		}
	}()

	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		var message protocol.Message
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			log.Printf("invalid JSON from %s: %v", conn.RemoteAddr(), err)
			continue
		}

		if message.Type == protocol.MsgHello {
			agent := protocol.CanonicalAgent(string(message.Agent))
			if agent == "" {
				continue
			}
			client.Agent = agent
			s.register(client)
			if s.handler != nil {
				s.handler.OnConnect(ctx, client)
			}
			continue
		}

		if client.Agent == "" {
			log.Printf("ignoring message before hello from %s", conn.RemoteAddr())
			continue
		}

		message.Agent = protocol.CanonicalAgent(string(message.Agent))
		if s.handler != nil {
			s.handler.OnMessage(ctx, client, message)
		}
	}

	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		log.Printf("connection error (%s): %v", client.Agent, err)
	}
}

func (s *Server) register(client *Client) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if old := s.clients[client.Agent]; old != nil && old != client {
		_ = old.Close()
	}
	s.clients[client.Agent] = client
}

func (s *Server) unregister(client *Client) {
	if client.Agent == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.clients[client.Agent] == client {
		delete(s.clients, client.Agent)
	}
}

func (s *Server) Send(ctx context.Context, agent protocol.AgentID, message protocol.Message) error {
	agent = protocol.CanonicalAgent(string(agent))

	s.mu.RLock()
	client := s.clients[agent]
	s.mu.RUnlock()

	if client == nil {
		return fmt.Errorf("%s is not connected", agent)
	}
	return client.Send(ctx, message)
}

func (s *Server) IsConnected(agent protocol.AgentID) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.clients[protocol.CanonicalAgent(string(agent))] != nil
}

func (s *Server) CloseAll() {
	s.mu.RLock()
	clients := make([]*Client, 0, len(s.clients))
	for _, client := range s.clients {
		clients = append(clients, client)
	}
	s.mu.RUnlock()

	for _, client := range clients {
		_ = client.Close()
	}
}
