package xduo

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

//go:embed web
var webAssets embed.FS

// WebServer owns only the local HTTP listener and browser connection. The
// manager and Duo sessions remain owned by tmux when the server stops.
type WebServer struct {
	URL           string
	AccessCode    string
	LANURLs       []string
	sessionKey    string
	loginFailures int
	loginBlocked  time.Time
	http          *http.Server
	mu            sync.Mutex
	client        *websocket.Conn
	closed        bool
	done          chan struct{}
	clients       sync.WaitGroup
}

func StartWeb(tm Tmux) (*WebServer, error) {
	return listenWeb(tm, "0.0.0.0:8787")
}

func listenWeb(tm Tmux, address string) (*WebServer, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("start browser server: %w", err)
	}
	code, key, err := webCredentials()
	if err != nil {
		listener.Close()
		return nil, err
	}
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	s := &WebServer{URL: "http://127.0.0.1:" + port + "/", AccessCode: code, sessionKey: key, LANURLs: lanWebURLs(port), done: make(chan struct{})}
	assets, _ := fs.Sub(webAssets, "web")
	files := http.FileServer(http.FS(assets))
	s.http = &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'")
		switch r.URL.Path {
		case "/api/session":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]bool{"authenticated": s.authenticated(r)})
			return
		case "/api/login":
			s.login(w, r)
			return
		case "/ws":
			if !sameWebOrigin(r) {
				http.Error(w, "Invalid origin", http.StatusForbidden)
				return
			}
			if !s.authenticated(r) {
				http.Error(w, "Access code required", http.StatusUnauthorized)
				return
			}
		default:
			files.ServeHTTP(w, r)
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			http.Error(w, "Server stopped", http.StatusServiceUnavailable)
			return
		}
		if s.client != nil {
			s.mu.Unlock()
			http.Error(w, "A browser is already connected", http.StatusConflict)
			return
		}
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			s.mu.Unlock()
			return
		}
		s.client = ws
		s.clients.Add(1)
		s.mu.Unlock()
		defer func() { ws.Close(); s.mu.Lock(); s.client = nil; s.mu.Unlock(); s.clients.Done() }()
		bridgeWebTerminal(tm, ws)
	})}
	go func() { defer close(s.done); _ = s.http.Serve(listener) }()
	return s, nil
}

func (s *WebServer) Close() error {
	s.mu.Lock()
	s.closed = true
	if s.client != nil {
		_ = s.client.Close()
	}
	s.mu.Unlock()
	err := s.http.Close()
	<-s.done
	s.clients.Wait()
	return err
}
