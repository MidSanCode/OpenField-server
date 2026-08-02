package handler

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/middleware"
)

const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = 54 * time.Second
	maxMsgSize = 4096
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// WsHandler upgrades a connection and wires it into the hub.
type WsHandler struct {
	hub *Hub
}

// NewWsHandler creates a WebSocket handler.
func NewWsHandler(hub *Hub) *WsHandler {
	return &WsHandler{hub: hub}
}

// Connect is the WebSocket endpoint. The gateway validates the JWT and sets
// X-User-ID, so the user is already trusted here.
func (h *WsHandler) Connect(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		logger.Log.Warn("websocket upgrade failed", "error", err)
		return
	}

	client := &Client{
		userID: userID,
		conn:   conn,
		send:   make(chan []byte, 256),
	}
	h.hub.register <- client

	// Send a greeting so the client knows the connection is alive.
	_ = client.conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"connected"}`))

	go h.writePump(client)
	go h.readPump(client)
}

// readPump keeps the connection alive and cleans up on disconnect.
func (h *WsHandler) readPump(c *Client) {
	defer func() {
		h.hub.unregister <- c
	}()
	c.conn.SetReadLimit(maxMsgSize)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			return
		}
	}
}

// writePump writes queued payloads and pings periodically.
func (h *WsHandler) writePump(c *Client) {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()
	for {
		select {
		case msg, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
