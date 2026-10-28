package xduo

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

//go:embed web
var webAssets embed.FS

// WebServer owns only the local HTTP listener and browser connection. The
// manager and Duo sessions remain owned by tmux when the server stops.
type WebServer struct {
	URL     string
	http    *http.Server
	mu      sync.Mutex
	client  *websocket.Conn
	closed  bool
	done    chan struct{}
	clients sync.WaitGroup
}

func StartWeb(tm Tmux) (*WebServer, error) {
	return listenWeb(tm, "127.0.0.1:8787")
}

func listenWeb(tm Tmux, address string) (*WebServer, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("start browser server: %w", err)
	}
	var secret [32]byte
	if _, err = rand.Read(secret[:]); err != nil {
		listener.Close()
		return nil, err
	}
	token := hex.EncodeToString(secret[:])
	host := listener.Addr().String()
	s := &WebServer{URL: "http://" + host + "/#" + token, done: make(chan struct{})}
	assets, _ := fs.Sub(webAssets, "web")
	files := http.FileServer(http.FS(assets))
	s.http = &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != host {
			http.Error(w, "Invalid host", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'")
		if r.URL.Path != "/ws" {
			files.ServeHTTP(w, r)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("token")), []byte(token)) != 1 {
			http.Error(w, "Invalid access link", http.StatusForbidden)
			return
		}
		origin, err := url.Parse(r.Header.Get("Origin"))
		if err != nil || origin.Scheme != "http" || origin.Host != host {
			http.Error(w, "Invalid origin", http.StatusForbidden)
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
