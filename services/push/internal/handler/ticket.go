package handler

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/middleware"
)

// wsTicketTTL is how long a WebSocket connection ticket remains valid. It only
// needs to cover the gap between the ticket request and the upgrade request,
// so a short lifetime keeps replay windows minimal.
const wsTicketTTL = 30 * time.Second

// ticketRecord binds one single-use connection ticket to its user.
type ticketRecord struct {
	userID    int64
	expiresAt time.Time
}

// ticketStore keeps short-lived single-use WebSocket tickets in memory.
// Tickets let browsers open the realtime socket without putting a long-lived
// JWT into the URL query string (which leaks into proxy and access logs):
// the client first POSTs /api/v1/ws with its Bearer token, then connects to
// /api/v1/ws?ticket=... within the TTL.
type ticketStore struct {
	mu      sync.Mutex
	tickets map[string]ticketRecord
	now     func() time.Time
}

func newTicketStore() *ticketStore {
	return &ticketStore{
		tickets: make(map[string]ticketRecord),
		now:     time.Now,
	}
}

// issue mints a random ticket bound to userID.
func (s *ticketStore) issue(userID int64) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	ticket := hex.EncodeToString(b)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	s.tickets[ticket] = ticketRecord{userID: userID, expiresAt: s.now().Add(wsTicketTTL)}
	return ticket, nil
}

// redeem consumes a valid ticket and reports its user. Unknown, expired and
// already-used tickets are rejected alike.
func (s *ticketStore) redeem(ticket string) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.tickets[ticket]
	if !ok {
		return 0, false
	}
	delete(s.tickets, ticket)
	if s.now().After(rec.expiresAt) {
		return 0, false
	}
	return rec.userID, true
}

// sweepLocked drops expired tickets. Caller must hold s.mu.
func (s *ticketStore) sweepLocked() {
	now := s.now()
	for t, rec := range s.tickets {
		if now.After(rec.expiresAt) {
			delete(s.tickets, t)
		}
	}
}

// tickets is the process-wide store used by the WS handlers.
var tickets = newTicketStore()

// TicketHandler serves POST /api/v1/ws: exchanges a gateway-authenticated
// request for a single-use connection ticket.
type TicketHandler struct{}

// Create issues a connection ticket for the authenticated user.
func (h *TicketHandler) Create(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	ticket, err := tickets.issue(userID)
	if err != nil {
		logger.Log.Error("failed to mint websocket ticket", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create ticket"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ticket":     ticket,
		"expires_in": int(wsTicketTTL.Seconds()),
	})
}
