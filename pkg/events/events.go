package events

import (
	"context"
	"encoding/json"

	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/logger"
)

// Channel is the Postgres NOTIFY channel used to fan out realtime events to the
// push (websocket) service. Services that mutate chat/posts data publish onto
// this channel; the push service listens and relays to connected clients.
const Channel = "openfield_events"

// Event is the envelope published by backend services onto the shared channel.
type Event struct {
	Type       string          `json:"type"`
	Recipients []int64         `json:"recipients,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`
}

// Publish sends an event onto the shared Postgres channel. It is fire-and-forget:
// if notification fails the event is dropped (the DB write already succeeded).
func Publish(ctx context.Context, typ string, recipients []int64, data interface{}) {
	raw, err := json.Marshal(data)
	if err != nil {
		logger.Log.Warn("failed to marshal push event", "type", typ, "error", err)
		return
	}
	payload, err := json.Marshal(Event{Type: typ, Recipients: recipients, Data: raw})
	if err != nil {
		logger.Log.Warn("failed to marshal push envelope", "type", typ, "error", err)
		return
	}
	if _, err := database.DB.ExecContext(ctx, "SELECT pg_notify($1, $2)", Channel, string(payload)); err != nil {
		logger.Log.Warn("failed to publish push event", "type", typ, "error", err)
		return
	}
	logger.Log.Debug("published push event", "type", typ, "recipients", len(recipients))
}

// EventType constants shared between publishers and the push service.
const (
	ChatMessageCreated = "chat.message.created"
	ChatMessageUpdated = "chat.message.updated"
	ChatMessageDeleted = "chat.message.deleted"

	PostCreated = "post.created"
	PostDeleted = "post.deleted"

	ReplyCreated = "post.reply.created"
	ReplyUpdated = "post.reply.updated"
	ReplyDeleted = "post.reply.deleted"

	ConversationUpdated = "chat.conversation.updated"
	ConsentRequested    = "chat.consent.requested"
	ChatTyping          = "chat.typing"
)
