package handler

import (
	"encoding/json"

	"github.com/lib/pq"
	"github.com/openfield/server/pkg/logger"
)

// eventEnvelope is the on-the-wire notification payload published by backend
// services onto the shared Postgres channel.
type eventEnvelope struct {
	Type       string          `json:"type"`
	Recipients []int64         `json:"recipients,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`
}

// listener relays Postgres NOTIFY events onto the hub. It must run in its own
// goroutine. It reconnects automatically via pq.Listener.
type listener struct {
	hub *Hub
	dsn string
	ln  *pq.Listener
}

// StartListener opens a dedicated Postgres connection and LISTENs on the
// openfield_events channel, forwarding each event to the hub.
func StartListener(hub *Hub, dsn string) (*listener, error) {
	l := &listener{hub: hub, dsn: dsn}
	ln := pq.NewListener(dsn, 1e9, 10e9, func(ev pq.ListenerEventType, err error) {
		if err != nil {
			logger.Log.Warn("push listener connection error", "event", ev, "error", err)
		}
	})
	l.ln = ln
	if err := ln.Listen("openfield_events"); err != nil {
		ln.Close()
		return nil, err
	}
	logger.Log.Info("push listener listening", "channel", "openfield_events")
	go l.run(ln)
	return l, nil
}

// Close stops the listener connection.
func (l *listener) Close() error {
	if l.ln != nil {
		return l.ln.Close()
	}
	return nil
}

func (l *listener) run(ln *pq.Listener) {
	for {
		select {
		case n, ok := <-ln.Notify:
			if !ok {
				return
			}
			l.handleNotification(n)
		}
	}
}

func (l *listener) handleNotification(n *pq.Notification) {
	if n == nil || n.Extra == "" {
		return
	}
	var env eventEnvelope
	if err := json.Unmarshal([]byte(n.Extra), &env); err != nil {
		logger.Log.Warn("failed to unmarshal push event", "error", err)
		return
	}
	payload := env.Data
	if len(payload) == 0 {
		// Re-encode the whole envelope so clients still get the type field.
		b, err := json.Marshal(env)
		if err != nil {
			return
		}
		payload = b
	}
	switch {
	case len(env.Recipients) == 0:
		l.hub.Broadcast(payload)
	default:
		l.hub.SendTo(env.Recipients, payload)
	}
}
