package handler

import (
	"sync"

	"github.com/gorilla/websocket"
	"github.com/openfield/server/pkg/logger"
)

// Client is a single connected WebSocket client.
type Client struct {
	userID int64
	conn   *websocket.Conn
	send   chan []byte
}

// Hub tracks all connected clients by user ID and relays events to them.
type Hub struct {
	mu        sync.RWMutex
	clients   map[*Client]struct{}
	byUser    map[int64]map[*Client]struct{}
	register  chan *Client
	unregister chan *Client
	quit      chan struct{}
}

// NewHub creates an empty hub.
func NewHub() *Hub {
	return &Hub{
		clients:    make(map[*Client]struct{}),
		byUser:     make(map[int64]map[*Client]struct{}),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		quit:       make(chan struct{}),
	}
}

// Run starts the hub's event loop. It must run in its own goroutine.
func (h *Hub) Run() {
	for {
		select {
		case c := <-h.register:
			h.mu.Lock()
			h.clients[c] = struct{}{}
			if h.byUser[c.userID] == nil {
				h.byUser[c.userID] = make(map[*Client]struct{})
			}
			h.byUser[c.userID][c] = struct{}{}
			h.mu.Unlock()
		case c := <-h.unregister:
			h.removeClient(c)
		case <-h.quit:
			h.mu.Lock()
			for c := range h.clients {
				c.conn.Close()
				close(c.send)
			}
			h.mu.Unlock()
			return
		}
	}
}

func (h *Hub) removeClient(c *Client) {
	h.mu.Lock()
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		if users := h.byUser[c.userID]; users != nil {
			delete(users, c)
			if len(users) == 0 {
				delete(h.byUser, c.userID)
			}
		}
		h.mu.Unlock()
		close(c.send)
		c.conn.Close()
		return
	}
	h.mu.Unlock()
}

// Broadcast sends a payload to all connected clients.
func (h *Hub) Broadcast(payload []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		h.enqueue(c, payload)
	}
}

// SendTo sends a payload to all clients for the given user IDs.
func (h *Hub) SendTo(userIDs []int64, payload []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, uid := range userIDs {
		for c := range h.byUser[uid] {
			h.enqueue(c, payload)
		}
	}
}

func (h *Hub) enqueue(c *Client, payload []byte) {
	select {
	case c.send <- payload:
	default:
		// Slow client: drop the connection to avoid unbounded buffering.
		logger.Log.Debug("dropping slow websocket client", "user_id", c.userID)
		go func() {
			h.unregister <- c
		}()
	}
}

// Close shuts the hub down.
func (h *Hub) Close() {
	close(h.quit)
}
