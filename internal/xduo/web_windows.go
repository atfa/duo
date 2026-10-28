//go:build windows

package xduo

import "github.com/gorilla/websocket"

func bridgeWebTerminal(_ Tmux, ws *websocket.Conn) {
	_ = ws.WriteMessage(websocket.BinaryMessage, []byte("xduo's tmux backend requires Linux.\r\n"))
}
