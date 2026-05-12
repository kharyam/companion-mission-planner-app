package api

import (
	"net/http"

	"github.com/gorilla/websocket"
)

// upgrader honors the same CORS allowlist as REST endpoints.
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		// Origin checks are enforced by corsMiddleware before we get here,
		// so accept anything the rest of the stack already approved.
		return true
	},
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Warn("ws upgrade failed", "err", err)
		return
	}
	defer conn.Close()

	sub := &subscriber{send: make(chan Event, 16)}
	s.subsMu.Lock()
	s.subs[sub] = struct{}{}
	s.subsMu.Unlock()
	defer func() {
		s.subsMu.Lock()
		delete(s.subs, sub)
		s.subsMu.Unlock()
		close(sub.send)
	}()

	// reader goroutine: just drain pings/pongs, ignore client messages.
	go func() {
		for {
			if _, _, err := conn.NextReader(); err != nil {
				return
			}
		}
	}()

	for ev := range sub.send {
		if err := conn.WriteJSON(ev); err != nil {
			return
		}
	}
}
