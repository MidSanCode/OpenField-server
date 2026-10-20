package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/middleware"
	"github.com/openfield/server/pkg/repository"
)

// TestMessageAuthorizationHTTPE2E locks the two authorization rules the audit
// found missing on message creation:
//
//   - a reply may only quote a message from the conversation it is written to,
//     because the preview is rendered from that row — an unbound id let a caller
//     walk the global id space and read other conversations' messages;
//   - an attachment may only be attached by the user who uploaded it, because
//     the message path re-signs attachment URLs with server credentials.
//
// Both were reachable through the normal send endpoint, so both are asserted
// through the real router, the real middleware and the real database.
func TestMessageAuthorizationHTTPE2E(t *testing.T) {
	connectHandlerE2E(t)
	gin.SetMode(gin.TestMode)

	convRepo := repository.NewConversationRepository()
	sender := newHandlerE2EUser(t, "e2e_authz_sender", false)
	peer := newHandlerE2EUser(t, "e2e_authz_peer", false)
	stranger := newHandlerE2EUser(t, "e2e_authz_stranger", false)

	conv, err := convRepo.CreatePrivate(sender, peer, false)
	if err != nil {
		t.Fatalf("creating the conversation failed: %v", err)
	}
	cleanupHTTPE2E(t, conv.ID)

	// A second conversation the sender is not part of: its messages are the
	// ones an unbound reply would have exposed.
	other, err := convRepo.CreatePrivate(stranger, peer, false)
	if err != nil {
		t.Fatalf("creating the other conversation failed: %v", err)
	}
	cleanupHTTPE2E(t, other.ID)

	r := gin.New()
	r.Use(middleware.Recovery())
	RegisterRoutes(r, NewConversationHandler(), NewConsentHandler(), NewMessageHandler(nil),
		NewGroupExtrasHandler(nil), NewCallHandler(nil))

	messagesPath := func(convID int64) string {
		return fmt.Sprintf("/api/v1/conversations/%d/messages", convID)
	}

	if w := callRequest(t, r, http.MethodPost, messagesPath(other.ID), stranger,
		map[string]any{"content": "a secret in another conversation"}); w.Code != http.StatusCreated {
		t.Fatalf("seeding the other conversation: status = %d, body = %s", w.Code, w.Body.String())
	}
	foreignID := lastMessageID(t, other.ID)

	// ---- the reply must belong to the conversation it is written to ----
	before := countMessages(t, conv.ID)
	if w := callRequest(t, r, http.MethodPost, messagesPath(conv.ID), sender,
		map[string]any{"content": "peek", "reply_to_id": foreignID}); w.Code != http.StatusBadRequest {
		t.Errorf("replying across conversations: status = %d, want 400 (body %s)", w.Code, w.Body.String())
	}
	if after := countMessages(t, conv.ID); after != before {
		t.Errorf("a refused reply still stored a message (%d → %d rows)", before, after)
	}
	// An id that does not exist gets the same answer, so ids cannot be probed.
	if w := callRequest(t, r, http.MethodPost, messagesPath(conv.ID), sender,
		map[string]any{"content": "peek", "reply_to_id": int64(999999999)}); w.Code != http.StatusBadRequest {
		t.Errorf("replying to a missing message: status = %d, want 400", w.Code)
	}

	// A reply inside the conversation is still accepted and previewed.
	if w := callRequest(t, r, http.MethodPost, messagesPath(conv.ID), sender,
		map[string]any{"content": "the original"}); w.Code != http.StatusCreated {
		t.Fatalf("sending the reply target: status = %d, body = %s", w.Code, w.Body.String())
	}
	localID := lastMessageID(t, conv.ID)
	if w := callRequest(t, r, http.MethodPost, messagesPath(conv.ID), sender,
		map[string]any{"content": "the reply", "reply_to_id": localID}); w.Code != http.StatusCreated {
		t.Fatalf("replying within the conversation: status = %d, body = %s", w.Code, w.Body.String())
	}
	if got := replyPreviewOf(t, r, conv.ID, peer, "the reply"); got != "the original" {
		t.Errorf("reply preview = %q, want %q", got, "the original")
	}

	// Read-side defence in depth: a row written before this check existed must
	// not leak a preview either.
	if _, err := database.DB.Exec(
		`INSERT INTO messages (conversation_id, sender_id, kind, content, reply_to_id)
		 VALUES ($1, $2, 'text', 'legacy reply', $3)`, conv.ID, sender, foreignID,
	); err != nil {
		t.Fatalf("inserting the legacy reply row failed: %v", err)
	}
	if got := replyPreviewOf(t, r, conv.ID, peer, "legacy reply"); got != "" {
		t.Errorf("legacy cross-conversation reply leaked a preview: %q", got)
	}

	// ---- attachments may only be attached by their uploader ----
	peerAttachment := newAttachment(t, peer)
	beforeLinks := countMessageAttachments(t, peerAttachment)
	if w := callRequest(t, r, http.MethodPost, messagesPath(conv.ID), sender,
		map[string]any{"content": "look at this", "attachment_ids": []int64{peerAttachment}}); w.Code != http.StatusBadRequest {
		t.Errorf("attaching someone else's file: status = %d, want 400 (body %s)", w.Code, w.Body.String())
	}
	if after := countMessageAttachments(t, peerAttachment); after != beforeLinks {
		t.Errorf("a refused attachment still created %d link row(s)", after-beforeLinks)
	}
	// An id that does not exist gets the same answer.
	if w := callRequest(t, r, http.MethodPost, messagesPath(conv.ID), sender,
		map[string]any{"content": "look", "attachment_ids": []int64{999999999}}); w.Code != http.StatusBadRequest {
		t.Errorf("attaching a missing id: status = %d, want 400", w.Code)
	}

	// The uploader's own attachment still works.
	ownAttachment := newAttachment(t, sender)
	if w := callRequest(t, r, http.MethodPost, messagesPath(conv.ID), sender,
		map[string]any{"content": "mine", "attachment_ids": []int64{ownAttachment}}); w.Code != http.StatusCreated {
		t.Errorf("attaching an own file: status = %d, want 201 (body %s)", w.Code, w.Body.String())
	}
	if got := countMessageAttachments(t, ownAttachment); got != 1 {
		t.Errorf("own attachment links = %d, want 1", got)
	}

	// More than one message's worth of files is refused instead of fanning out.
	tooMany := make([]int64, 0, 10)
	for i := 0; i < 10; i++ {
		tooMany = append(tooMany, newAttachment(t, sender))
	}
	if w := callRequest(t, r, http.MethodPost, messagesPath(conv.ID), sender,
		map[string]any{"content": "everything", "attachment_ids": tooMany}); w.Code != http.StatusBadRequest {
		t.Errorf("attaching 10 files: status = %d, want 400", w.Code)
	}
}

// lastMessageID returns the newest message id in a conversation.
func lastMessageID(t *testing.T, conversationID int64) int64 {
	t.Helper()
	var id int64
	if err := database.DB.QueryRow(
		"SELECT id FROM messages WHERE conversation_id = $1 ORDER BY id DESC LIMIT 1", conversationID,
	).Scan(&id); err != nil {
		t.Fatalf("loading the newest message of conversation %d failed: %v", conversationID, err)
	}
	return id
}

func countMessages(t *testing.T, conversationID int64) int {
	t.Helper()
	var n int
	if err := database.DB.QueryRow(
		"SELECT COUNT(*) FROM messages WHERE conversation_id = $1", conversationID,
	).Scan(&n); err != nil {
		t.Fatalf("counting messages failed: %v", err)
	}
	return n
}

// replyPreviewOf reads the conversation list as viewer and returns the preview
// attached to the message with the given content.
func replyPreviewOf(t *testing.T, r *gin.Engine, conversationID, viewer int64, content string) string {
	t.Helper()
	w := callRequest(t, r, http.MethodGet, fmt.Sprintf("/api/v1/conversations/%d/messages", conversationID), viewer, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("listing messages: status = %d, body = %s", w.Code, w.Body.String())
	}
	return replyPreviewFromBody(t, w, content)
}

func replyPreviewFromBody(t *testing.T, w *httptest.ResponseRecorder, content string) string {
	t.Helper()
	var envelope struct {
		Messages []struct {
			Content        string `json:"content"`
			ReplyToContent string `json:"reply_to_content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decoding the message list failed: %v", err)
	}
	for _, m := range envelope.Messages {
		if m.Content == content {
			return m.ReplyToContent
		}
	}
	t.Fatalf("message %q not found in the listing", content)
	return ""
}

// newAttachment stores an attachment row owned by userID.
func newAttachment(t *testing.T, userID int64) int64 {
	t.Helper()
	var id int64
	key := fmt.Sprintf("e2e/%d-%s.bin", userID, t.Name())
	if err := database.DB.QueryRow(
		`INSERT INTO attachments (user_id, object_key, original_name, url)
		 VALUES ($1, $2, 'e2e.bin', 'http://example.invalid/e2e.bin') RETURNING id`,
		userID, key,
	).Scan(&id); err != nil {
		t.Fatalf("creating an attachment failed: %v", err)
	}
	t.Cleanup(func() { database.DB.Exec("DELETE FROM attachments WHERE id = $1", id) })
	return id
}

func countMessageAttachments(t *testing.T, attachmentID int64) int {
	t.Helper()
	var n int
	if err := database.DB.QueryRow(
		"SELECT COUNT(*) FROM message_attachments WHERE attachment_id = $1", attachmentID,
	).Scan(&n); err != nil {
		t.Fatalf("counting message attachments failed: %v", err)
	}
	return n
}
