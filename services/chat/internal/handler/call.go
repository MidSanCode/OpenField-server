package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/config"
	"github.com/openfield/server/pkg/events"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/middleware"
	"github.com/openfield/server/pkg/model"
	"github.com/openfield/server/pkg/repository"
)

// CallHandler implements peer-to-peer voice/video call signalling.
//
// The server does three things and nothing else: it owns the call state machine
// (ringing → active → ended), it enforces one live call per conversation, and it
// relays opaque SDP/ICE payloads between the participants. Media never reaches
// the server, which is what makes the call peer-to-peer (see docs/calls.md).
//
// Signalling travels over REST rather than the existing WebSocket because that
// socket is downstream-only: push's readPump discards inbound frames, so a
// client has no way to write a candidate up it.
type CallHandler struct {
	convRepo *repository.ConversationRepository
	callRepo *repository.CallRepository
	msgRepo  *repository.MessageRepository
	// iceServers is the operator's ICE configuration, resolved once at startup
	// from the call section of the config file.
	iceServers []map[string]string
	turnSet    bool
}

// NewCallHandler creates a new CallHandler. A nil config is allowed and yields
// the built-in STUN defaults, which keeps tests and single-binary runs working
// without a config file.
func NewCallHandler(cfg *config.Config) *CallHandler {
	h := &CallHandler{
		convRepo: repository.NewConversationRepository(),
		callRepo: repository.NewCallRepository(),
		msgRepo:  repository.NewMessageRepository(),
	}
	if cfg != nil {
		h.iceServers = cfg.Call.ICEServers()
		h.turnSet = cfg.Call.HasTURN()
	} else {
		h.iceServers = config.CallConfig{}.ICEServers()
	}
	return h
}

// IceServers reports the ICE configuration clients should use for their peer
// connections.
//
// The server owns this so an operator configures the relay once instead of every
// user filling in a settings field. A response without a TURN entry means calls
// will fail between two peers that both sit behind a symmetric NAT — the client
// is told so it can warn instead of leaving the user staring at "connecting".
func (h *CallHandler) IceServers(c *gin.Context) {
	servers := h.iceServers
	if servers == nil {
		servers = []map[string]string{}
	}
	c.JSON(http.StatusOK, gin.H{
		"ice_servers": servers,
		"turn":        h.turnSet,
	})
}

// callSignalTypes are the only signalling messages a client may relay. They are
// passed through verbatim — the server never parses or validates the payload.
var callSignalTypes = map[string]bool{
	"offer":     true,
	"answer":    true,
	"candidate": true,
}

type startCallRequest struct {
	ConversationID int64  `json:"conversation_id"`
	Mode           string `json:"mode"`
}

type callSignalRequest struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// Start opens a call in a one-to-one conversation and rings the peer.
//
// Group conversations are rejected for now: a group call needs either a mesh
// (one peer connection per pair) or an SFU, and neither is implemented. Refusing
// loudly beats ringing a group that cannot be joined.
func (h *CallHandler) Start(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req startCallRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if req.ConversationID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "conversation_id is required"})
		return
	}

	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = model.CallModeAudio
	}
	if mode != model.CallModeAudio && mode != model.CallModeVideo {
		c.JSON(http.StatusBadRequest, gin.H{"error": "mode must be audio or video"})
		return
	}

	conv, err := h.convRepo.GetByID(req.ConversationID)
	if err != nil {
		logger.Log.Error("failed to load conversation for call", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start call"})
		return
	}
	if conv == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "conversation not found"})
		return
	}
	if conv.Type != "private" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "calls are only supported in one-to-one conversations"})
		return
	}

	isMember, err := h.convRepo.IsMember(req.ConversationID, userID)
	if err != nil {
		logger.Log.Error("failed to check membership for call", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start call"})
		return
	}
	if !isMember {
		c.JSON(http.StatusForbidden, gin.H{"error": "you are not a member of this conversation"})
		return
	}

	// A bot has no media endpoint: it can never speak, so a bot-initiated call
	// is pure ringing noise for the callee. Bots are ordinary accounts
	// everywhere else, so this is the one place they are refused outright.
	if repository.RequesterIsBot(userID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "bot accounts cannot place calls"})
		return
	}

	peerID, err := h.convRepo.GetPrivatePeer(req.ConversationID, userID)
	if err != nil || peerID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "could not resolve the other participant"})
		return
	}

	call, err := h.callRepo.CreateRinging(req.ConversationID, userID, mode, []int64{peerID})
	if err != nil {
		if errors.Is(err, repository.ErrCallAlreadyActive) {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		logger.Log.Error("failed to create call", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start call"})
		return
	}

	events.Publish(c.Request.Context(), events.CallInvited, []int64{peerID}, gin.H{
		"call_id":         call.ID,
		"conversation_id": call.ConversationID,
		"initiator_id":    userID,
		"mode":            call.Mode,
		"created_at":      call.CreatedAt,
	})

	c.JSON(http.StatusCreated, gin.H{"call": call})
}

// Get returns the call state. Clients call it when they join a conversation,
// reconnect, or suspect they missed an event — there is no event replay, so the
// state has to be pullable.
func (h *CallHandler) Get(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	callID, ok := parseCallID(c)
	if !ok {
		return
	}

	call, ok := h.loadCallForParticipant(c, callID, userID)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{"call": call})
}

// GetActiveForConversation reports the live call of a conversation, or null.
//
// Clients need this when they open a chat or reconnect: pushes are best-effort
// and there is no event replay, so without a way to ask "is there a call here?"
// a client that missed call.invited would never learn about it (and could not
// even guess the call id to GET /calls/:id).
func (h *CallHandler) GetActiveForConversation(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	convID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || convID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid conversation ID"})
		return
	}

	isMember, err := h.convRepo.IsMember(convID, userID)
	if err != nil {
		logger.Log.Error("failed to check membership for active call", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load call"})
		return
	}
	if !isMember {
		c.JSON(http.StatusForbidden, gin.H{"error": "you are not a member of this conversation"})
		return
	}

	call, err := h.callRepo.GetLiveForConversation(convID)
	if err != nil {
		logger.Log.Error("failed to load live call", "error", err, "conversation_id", convID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load call"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"call": call})
}

// Answer accepts a ringing call. Only one device can win: the update is
// conditional on the call still ringing, and the loser gets 409 with the current
// state so its UI can stop ringing instead of pretending to be connected.
func (h *CallHandler) Answer(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	callID, ok := parseCallID(c)
	if !ok {
		return
	}
	call, ok := h.loadCallForParticipant(c, callID, userID)
	if !ok {
		return
	}
	// Answering your own outgoing call would mark the caller as the accepted
	// party while the callee keeps ringing, so refuse it explicitly.
	if call.InitiatorID == userID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "you cannot answer your own call"})
		return
	}

	answered, err := h.callRepo.Answer(callID, userID)
	if err != nil {
		logger.Log.Error("failed to answer call", "error", err, "call_id", callID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to answer call"})
		return
	}
	if !answered {
		call, err := h.callRepo.Get(callID)
		if err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": "call is no longer ringing"})
			return
		}
		c.JSON(http.StatusConflict, gin.H{"error": "call is no longer ringing", "call": call})
		return
	}

	// Tell every participant, not just the caller: the callee's other devices
	// are also ringing and must stop.
	h.publishToParticipants(c.Request.Context(), events.CallAccepted, callID, gin.H{
		"call_id": callID,
		"user_id": userID,
	})

	updated, err := h.callRepo.Get(callID)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"status": "answered"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"call": updated})
}

// Decline rejects a ringing call. In a one-to-one conversation a decline ends
// the attempt outright; once group calls exist this becomes "leave the ringing
// set" instead.
func (h *CallHandler) Decline(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	callID, ok := parseCallID(c)
	if !ok {
		return
	}
	call, ok := h.loadCallForParticipant(c, callID, userID)
	if !ok {
		return
	}

	if err := h.callRepo.SetParticipantState(callID, userID, model.CallParticipantDeclined); err != nil {
		logger.Log.Error("failed to record call decline", "error", err, "call_id", callID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to decline call"})
		return
	}

	events.Publish(c.Request.Context(), events.CallDeclined, []int64{call.InitiatorID}, gin.H{
		"call_id": callID,
		"user_id": userID,
	})

	h.endCall(c.Request.Context(), callID, "declined")
	c.JSON(http.StatusOK, gin.H{"status": "declined"})
}

// Hangup ends the call from either side. While the call is still ringing this is
// a cancel (the caller gave up); afterwards it ends the conversation.
func (h *CallHandler) Hangup(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	callID, ok := parseCallID(c)
	if !ok {
		return
	}
	call, ok := h.loadCallForParticipant(c, callID, userID)
	if !ok {
		return
	}

	reason := "hung_up"
	if call.State == model.CallStateRinging {
		reason = "cancelled"
	}
	if err := h.callRepo.SetParticipantState(callID, userID, model.CallParticipantLeft); err != nil {
		logger.Log.Warn("failed to mark call participant left", "error", err, "call_id", callID)
	}

	h.endCall(c.Request.Context(), callID, reason)
	c.JSON(http.StatusOK, gin.H{"status": "ended"})
}

// Signal relays one opaque negotiation payload (SDP offer/answer or an ICE
// candidate) to the other participants. Nothing is stored: candidate exchange is
// high-frequency and loses all value once the call ends, and the payload size is
// capped so it always fits in a single NOTIFY.
func (h *CallHandler) Signal(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	callID, ok := parseCallID(c)
	if !ok {
		return
	}
	call, ok := h.loadCallForParticipant(c, callID, userID)
	if !ok {
		return
	}
	if call.State == model.CallStateEnded {
		c.JSON(http.StatusConflict, gin.H{"error": "call has ended"})
		return
	}

	var req callSignalRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if !callSignalTypes[req.Type] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "type must be offer, answer or candidate"})
		return
	}
	if len(req.Payload) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "payload is required"})
		return
	}
	if len(req.Payload) > repository.MaxSignalBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{
			"error": "signalling payload too large",
			"limit": repository.MaxSignalBytes,
		})
		return
	}

	recipients, err := h.otherParticipants(callID, userID)
	if err != nil {
		logger.Log.Error("failed to list call participants", "error", err, "call_id", callID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to relay signal"})
		return
	}
	if len(recipients) == 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "no other participant to signal"})
		return
	}

	events.Publish(c.Request.Context(), events.CallSignal, recipients, gin.H{
		"call_id":      callID,
		"from_user_id": userID,
		"type":         req.Type,
		"payload":      req.Payload,
	})

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// StartSweeper cancels calls that have been ringing for too long. Without it a
// caller who kills the app leaves the callee ringing forever and blocks the
// conversation, because idx_calls_live allows only one live call at a time.
func (h *CallHandler) StartSweeper(interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			swept, err := h.callRepo.SweepRinging()
			if err != nil {
				logger.Log.Error("call sweeper failed", "error", err)
				continue
			}
			for i := range swept {
				call := swept[i]
				recipients := make([]int64, 0, len(call.Participants))
				for _, p := range call.Participants {
					recipients = append(recipients, p.UserID)
				}
				events.Publish(context.Background(), events.CallEnded, recipients, gin.H{
					"call_id":          call.ID,
					"conversation_id":  call.ConversationID,
					"end_reason":       call.EndReason,
					"duration_seconds": call.DurationSeconds,
				})
				// A ringing call that ran out of time is a missed call on both
				// sides, so it gets a history entry like any other finished call.
				h.recordCall(context.Background(), &call, model.CallOutcomeMissed)
			}
		}
	}()
}

// endCall ends the call and publishes "call ended" exactly once (End is
// conditional on the call not being ended yet).
func (h *CallHandler) endCall(ctx context.Context, callID int64, reason string) {
	ended, err := h.callRepo.End(callID, reason)
	if err != nil {
		logger.Log.Error("failed to end call", "error", err, "call_id", callID)
		return
	}
	if !ended {
		// Someone else already ended it and published the event.
		return
	}

	call, err := h.callRepo.Get(callID)
	if err != nil {
		logger.Log.Warn("failed to load ended call for event", "error", err, "call_id", callID)
		return
	}
	recipients, err := h.callRepo.ParticipantIDs(callID)
	if err != nil {
		logger.Log.Warn("failed to list participants for call end event", "error", err, "call_id", callID)
		return
	}
	events.Publish(ctx, events.CallEnded, recipients, gin.H{
		"call_id":          call.ID,
		"conversation_id":  call.ConversationID,
		"end_reason":       call.EndReason,
		"duration_seconds": call.DurationSeconds,
	})
	h.recordCall(ctx, call, callOutcome(call))
}

// callOutcome classifies how a finished call ended, for its history entry.
func callOutcome(call *model.CallSession) string {
	if call.DurationSeconds > 0 || call.AnsweredAt != nil {
		return model.CallOutcomeAnswered
	}
	switch call.EndReason {
	case "declined":
		return model.CallOutcomeDeclined
	case "timeout":
		return model.CallOutcomeMissed
	default:
		// "cancelled" and a "hung_up" that never reached an answer share one
		// wording: the call did not take place and nobody rejected it.
		return model.CallOutcomeCancelled
	}
}

// recordCall appends a call history message to the conversation and pushes it to
// the members.
//
// Failures are logged but never surfaced: a missing history entry must not turn
// a successful hangup into an error for the caller.
func (h *CallHandler) recordCall(ctx context.Context, call *model.CallSession, outcome string) {
	if h.msgRepo == nil || call == nil {
		return
	}
	record := model.CallRecord{
		CallID:          call.ID,
		Mode:            call.Mode,
		Outcome:         outcome,
		DurationSeconds: int(call.DurationSeconds),
		InitiatorID:     call.InitiatorID,
	}
	msg, err := h.msgRepo.CreateCallRecord(call.ConversationID, call.InitiatorID, record)
	if err != nil {
		logger.Log.Warn("failed to record the finished call", "error", err, "call_id", call.ID)
		return
	}
	h.publishToConversation(ctx, events.ChatMessageCreated, call.ConversationID, msg)
}

// publishToConversation pushes an event to every active member of a conversation,
// mirroring what the HTTP message handlers do after a write.
func (h *CallHandler) publishToConversation(ctx context.Context, typ string, convID int64, payload interface{}) {
	members, err := h.convRepo.ListMembers(convID)
	if err != nil {
		logger.Log.Warn("failed to list members for push", "error", err, "conversation_id", convID)
		return
	}
	recipients := make([]int64, 0, len(members))
	for _, m := range members {
		if m.Status == "active" {
			recipients = append(recipients, m.UserID)
		}
	}
	events.Publish(ctx, typ, recipients, payload)
}

// publishToParticipants pushes an event to the whole call roster.
func (h *CallHandler) publishToParticipants(ctx context.Context, typ string, callID int64, payload interface{}) {
	recipients, err := h.callRepo.ParticipantIDs(callID)
	if err != nil {
		logger.Log.Warn("failed to list participants for call event", "error", err, "call_id", callID)
		return
	}
	events.Publish(ctx, typ, recipients, payload)
}

// otherParticipants returns everyone on the roster except the caller.
func (h *CallHandler) otherParticipants(callID, userID int64) ([]int64, error) {
	participants, err := h.callRepo.ListParticipants(callID)
	if err != nil {
		return nil, err
	}
	out := make([]int64, 0, len(participants))
	for _, p := range participants {
		if p.UserID != userID {
			out = append(out, p.UserID)
		}
	}
	return out, nil
}

// loadCallForParticipant resolves the call and enforces that the requester is on
// its roster. It writes the error response itself and reports whether the caller
// may continue.
func (h *CallHandler) loadCallForParticipant(c *gin.Context, callID, userID int64) (*model.CallSession, bool) {
	call, err := h.callRepo.Get(callID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "call not found"})
			return nil, false
		}
		logger.Log.Error("failed to load call", "error", err, "call_id", callID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load call"})
		return nil, false
	}

	isParticipant, err := h.callRepo.IsParticipant(callID, userID)
	if err != nil {
		logger.Log.Error("failed to check call participation", "error", err, "call_id", callID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load call"})
		return nil, false
	}
	if !isParticipant {
		c.JSON(http.StatusForbidden, gin.H{"error": "you are not a participant of this call"})
		return nil, false
	}
	return call, true
}

// parseCallID reads the :id parameter, writing a 400 when it is not a number.
func parseCallID(c *gin.Context) (int64, bool) {
	callID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || callID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid call ID"})
		return 0, false
	}
	return callID, true
}
