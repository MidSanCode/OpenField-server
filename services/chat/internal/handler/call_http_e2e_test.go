package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/config"
	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/middleware"
	"github.com/openfield/server/pkg/model"
	"github.com/openfield/server/pkg/repository"
)

// connectHandlerE2E wires the chat service to a scratch PostgreSQL database so
// handler tests can run the real routes, the real gateway auth middleware and
// the real repositories. Nothing is stubbed except the object store.
//
//	OF_REPO_E2E=1 OF_REPO_E2E_DB=of_scratch go test ./services/chat/...
func connectHandlerE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("OF_REPO_E2E") != "1" {
		t.Skip("set OF_REPO_E2E=1 and OF_REPO_E2E_DB=<scratch db> to run")
	}
	dbName := os.Getenv("OF_REPO_E2E_DB")
	if dbName == "" {
		t.Fatal("OF_REPO_E2E_DB must name the scratch database")
	}
	if logger.Log == nil {
		logger.Init()
	}
	cfg := &config.Config{Database: config.DatabaseConfig{
		Host:     "127.0.0.1",
		Port:     5432,
		User:     "of-user",
		Password: "of-user-1207",
		DBName:   dbName,
		SSLMode:  "disable",
	}}
	if err := database.Connect(cfg); err != nil {
		t.Fatalf("connect to scratch database failed: %v", err)
	}
	t.Cleanup(database.Close)
	if err := database.RunMigrations(); err != nil {
		t.Fatalf("migrating the scratch database failed: %v", err)
	}
}

// newHandlerE2EUser inserts a throwaway user and removes it afterwards.
func newHandlerE2EUser(t *testing.T, prefix string, isBot bool) int64 {
	t.Helper()
	name := fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	var id int64
	if err := database.DB.QueryRow(
		"INSERT INTO users (username, is_bot) VALUES ($1, $2) RETURNING id", name, isBot,
	).Scan(&id); err != nil {
		t.Fatalf("creating user %s failed: %v", name, err)
	}
	t.Cleanup(func() { database.DB.Exec("DELETE FROM users WHERE id = $1", id) })
	return id
}

// callRequest performs one API call the way the gateway would: the authenticated
// user id arrives in the X-User-ID header.
func callRequest(t *testing.T, r *gin.Engine, method, path string, userID int64, body any) *httptest.ResponseRecorder {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			t.Fatalf("marshalling request body failed: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	if userID > 0 {
		req.Header.Set(middleware.UserIDHeader, fmt.Sprintf("%d", userID))
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// decodeCall pulls the "call" object out of a response body.
func decodeCall(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope struct {
		Call map[string]any `json:"call"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decoding response %q failed: %v", w.Body.String(), err)
	}
	return envelope.Call
}

// TestCallHTTPE2E exercises the call endpoints through the real router, the real
// auth middleware and the real database. It locks the contract the Flutter client
// is written against: status codes, which user may do what, and the state the
// server reports back.
func TestCallHTTPE2E(t *testing.T) {
	connectHandlerE2E(t)
	gin.SetMode(gin.TestMode)

	convRepo := repository.NewConversationRepository()
	caller := newHandlerE2EUser(t, "e2e_http_caller", false)
	callee := newHandlerE2EUser(t, "e2e_http_callee", false)
	outsider := newHandlerE2EUser(t, "e2e_http_outsider", false)
	bot := newHandlerE2EUser(t, "e2e_http_bot", true)

	conv, err := convRepo.CreatePrivate(caller, callee, false)
	if err != nil {
		t.Fatalf("creating the conversation failed: %v", err)
	}
	cleanupHTTPE2E(t, conv.ID)

	group, err := convRepo.CreateGroup(caller, fmt.Sprintf("e2e_http_group_%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("creating the group conversation failed: %v", err)
	}
	cleanupHTTPE2E(t, group.ID)

	botConv, err := convRepo.CreatePrivate(bot, callee, false)
	if err != nil {
		t.Fatalf("creating the bot conversation failed: %v", err)
	}
	cleanupHTTPE2E(t, botConv.ID)

	r := gin.New()
	r.Use(middleware.Recovery())
	// The object store is the only dependency these routes do not need; nil is
	// fine because no call endpoint touches attachments.
	RegisterRoutes(r, NewConversationHandler(), NewConsentHandler(), NewMessageHandler(nil),
		NewGroupExtrasHandler(nil), NewCallHandler(nil))

	// ---- auth ----
	if w := callRequest(t, r, http.MethodPost, "/api/v1/calls", 0, map[string]any{"conversation_id": conv.ID}); w.Code != http.StatusUnauthorized {
		t.Fatalf("call without X-User-ID: status = %d, want 401", w.Code)
	}

	// ---- validation ----
	if w := callRequest(t, r, http.MethodPost, "/api/v1/calls", caller, map[string]any{}); w.Code != http.StatusBadRequest {
		t.Errorf("call without conversation_id: status = %d, want 400", w.Code)
	}
	if w := callRequest(t, r, http.MethodPost, "/api/v1/calls", caller,
		map[string]any{"conversation_id": conv.ID, "mode": "hologram"}); w.Code != http.StatusBadRequest {
		t.Errorf("invalid mode: status = %d, want 400", w.Code)
	}
	if w := callRequest(t, r, http.MethodPost, "/api/v1/calls", caller,
		map[string]any{"conversation_id": 999999999}); w.Code != http.StatusNotFound {
		t.Errorf("unknown conversation: status = %d, want 404", w.Code)
	}
	if w := callRequest(t, r, http.MethodPost, "/api/v1/calls", outsider,
		map[string]any{"conversation_id": conv.ID}); w.Code != http.StatusForbidden {
		t.Errorf("non-member dialling: status = %d, want 403", w.Code)
	}
	if w := callRequest(t, r, http.MethodPost, "/api/v1/calls", caller,
		map[string]any{"conversation_id": group.ID}); w.Code != http.StatusBadRequest {
		t.Errorf("group conversation: status = %d, want 400", w.Code)
	}
	if w := callRequest(t, r, http.MethodPost, "/api/v1/calls", bot,
		map[string]any{"conversation_id": botConv.ID}); w.Code != http.StatusForbidden {
		t.Errorf("bot dialling: status = %d, want 403", w.Code)
	}

	// ---- open a call ----
	w := callRequest(t, r, http.MethodPost, "/api/v1/calls", caller,
		map[string]any{"conversation_id": conv.ID, "mode": "audio"})
	if w.Code != http.StatusCreated {
		t.Fatalf("opening a call: status = %d, want 201 (body %s)", w.Code, w.Body.String())
	}
	opened := decodeCall(t, w)
	if opened["state"] != "ringing" || opened["mode"] != "audio" {
		t.Fatalf("opened call = %v, want ringing/audio", opened)
	}
	callID := int64(opened["id"].(float64))

	// ---- a second dial is refused ----
	if w := callRequest(t, r, http.MethodPost, "/api/v1/calls", caller,
		map[string]any{"conversation_id": conv.ID}); w.Code != http.StatusConflict {
		t.Errorf("second dial: status = %d, want 409", w.Code)
	}

	// ---- the callee can discover it, an outsider cannot ----
	w = callRequest(t, r, http.MethodGet, fmt.Sprintf("/api/v1/conversations/%d/call", conv.ID), callee, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("live-call lookup: status = %d, want 200", w.Code)
	}
	if live := decodeCall(t, w); live == nil || int64(live["id"].(float64)) != callID {
		t.Fatalf("live-call lookup = %v, want call %d", live, callID)
	}
	if w := callRequest(t, r, http.MethodGet, fmt.Sprintf("/api/v1/calls/%d", callID), outsider, nil); w.Code != http.StatusForbidden {
		t.Errorf("outsider reading a call: status = %d, want 403", w.Code)
	}
	if w := callRequest(t, r, http.MethodGet, "/api/v1/calls/abc", caller, nil); w.Code != http.StatusBadRequest {
		t.Errorf("non-numeric call id: status = %d, want 400", w.Code)
	}
	if w := callRequest(t, r, http.MethodGet, fmt.Sprintf("/api/v1/calls/%d", callID+99999), caller, nil); w.Code != http.StatusNotFound {
		t.Errorf("unknown call: status = %d, want 404", w.Code)
	}

	// ---- signalling relay ----
	if w := callRequest(t, r, http.MethodPost, fmt.Sprintf("/api/v1/calls/%d/signal", callID), caller,
		map[string]any{"type": "offer", "payload": map[string]any{"sdp": "v=0..."}}); w.Code != http.StatusOK {
		t.Errorf("offer relay: status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if w := callRequest(t, r, http.MethodPost, fmt.Sprintf("/api/v1/calls/%d/signal", callID), caller,
		map[string]any{"type": "teleport", "payload": map[string]any{}}); w.Code != http.StatusBadRequest {
		t.Errorf("unknown signal type: status = %d, want 400", w.Code)
	}
	if w := callRequest(t, r, http.MethodPost, fmt.Sprintf("/api/v1/calls/%d/signal", callID), caller,
		map[string]any{"type": "offer"}); w.Code != http.StatusBadRequest {
		t.Errorf("signal without payload: status = %d, want 400", w.Code)
	}
	// The cap exists because a NOTIFY payload above 8000 bytes is rejected by
	// PostgreSQL, which would drop the signal silently.
	oversized := map[string]any{"type": "offer", "payload": map[string]any{"sdp": strings.Repeat("x", repository.MaxSignalBytes)}}
	if w := callRequest(t, r, http.MethodPost, fmt.Sprintf("/api/v1/calls/%d/signal", callID), caller, oversized); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized signal: status = %d, want 413", w.Code)
	}

	// ---- answering ----
	if w := callRequest(t, r, http.MethodPost, fmt.Sprintf("/api/v1/calls/%d/answer", callID), caller, nil); w.Code != http.StatusBadRequest {
		t.Errorf("initiator answering its own call: status = %d, want 400", w.Code)
	}
	if w := callRequest(t, r, http.MethodPost, fmt.Sprintf("/api/v1/calls/%d/answer", callID), outsider, nil); w.Code != http.StatusForbidden {
		t.Errorf("outsider answering: status = %d, want 403", w.Code)
	}
	w = callRequest(t, r, http.MethodPost, fmt.Sprintf("/api/v1/calls/%d/answer", callID), callee, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("answering: status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if answered := decodeCall(t, w); answered["state"] != "active" {
		t.Fatalf("answered call state = %v, want active", answered["state"])
	}
	// The second device loses the race but gets the current state back.
	w = callRequest(t, r, http.MethodPost, fmt.Sprintf("/api/v1/calls/%d/answer", callID), callee, nil)
	if w.Code != http.StatusConflict {
		t.Errorf("second answer: status = %d, want 409", w.Code)
	}
	if body := decodeCall(t, w); body == nil {
		t.Error("a losing answer should still return the current call state")
	}

	// ---- hangup and the final state ----
	// Let a second elapse so the recorded duration is measurably non-zero.
	time.Sleep(time.Second)
	if w := callRequest(t, r, http.MethodPost, fmt.Sprintf("/api/v1/calls/%d/hangup", callID), caller, nil); w.Code != http.StatusOK {
		t.Errorf("hangup: status = %d, want 200", w.Code)
	}
	w = callRequest(t, r, http.MethodGet, fmt.Sprintf("/api/v1/calls/%d", callID), callee, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("reading the ended call: status = %d, want 200", w.Code)
	}
	ended := decodeCall(t, w)
	if ended["state"] != "ended" || ended["end_reason"] != "hung_up" {
		t.Errorf("ended call = %v/%v, want ended/hung_up", ended["state"], ended["end_reason"])
	}
	if w := callRequest(t, r, http.MethodGet, fmt.Sprintf("/api/v1/conversations/%d/call", conv.ID), callee, nil); w.Code != http.StatusOK {
		t.Errorf("live-call lookup after hangup: status = %d, want 200", w.Code)
	} else if live := decodeCall(t, w); live != nil {
		t.Errorf("an ended call should not be reported as live, got %v", live)
	}

	// Idempotent hangup: a client retrying after a dropped response must not
	// turn a harmless retry into an error.
	if w := callRequest(t, r, http.MethodPost, fmt.Sprintf("/api/v1/calls/%d/hangup", callID), callee, nil); w.Code != http.StatusOK {
		t.Errorf("repeated hangup: status = %d, want 200", w.Code)
	}

	// ---- decline ends the attempt ----
	w = callRequest(t, r, http.MethodPost, "/api/v1/calls", caller, map[string]any{"conversation_id": conv.ID})
	if w.Code != http.StatusCreated {
		t.Fatalf("re-dialling after hangup: status = %d, want 201", w.Code)
	}
	secondID := int64(decodeCall(t, w)["id"].(float64))
	if w := callRequest(t, r, http.MethodPost, fmt.Sprintf("/api/v1/calls/%d/decline", secondID), callee, nil); w.Code != http.StatusOK {
		t.Errorf("declining: status = %d, want 200", w.Code)
	}
	w = callRequest(t, r, http.MethodGet, fmt.Sprintf("/api/v1/calls/%d", secondID), caller, nil)
	if declined := decodeCall(t, w); declined["state"] != "ended" || declined["end_reason"] != "declined" {
		t.Errorf("declined call = %v/%v, want ended/declined", declined["state"], declined["end_reason"])
	}

	// ---- call history entries ----
	// Every finished call leaves one message of kind "call" in the thread. The
	// content stays empty and the outcome travels in the payload, so clients can
	// phrase it themselves (and differently for caller and callee).
	msgs, err := repository.NewMessageRepository().ListByConversation(conv.ID, 0, 50)
	if err != nil {
		t.Fatalf("listing messages after the calls failed: %v", err)
	}
	records := make([]*model.CallRecord, 0, 2)
	for i := range msgs {
		if msgs[i].Kind != model.CallRecordKind {
			continue
		}
		if msgs[i].Call == nil {
			t.Fatalf("call message %d carries no payload", msgs[i].ID)
		}
		if msgs[i].Content != "" {
			t.Errorf("call message %d content = %q, want empty", msgs[i].ID, msgs[i].Content)
		}
		records = append(records, msgs[i].Call)
	}
	if len(records) != 2 {
		t.Fatalf("call history entries = %d, want 2", len(records))
	}
	// Index by outcome rather than by position: the thread is returned in
	// chronological order, which is an implementation detail this test has no
	// business pinning.
	byOutcome := make(map[string]*model.CallRecord, len(records))
	for _, record := range records {
		byOutcome[record.Outcome] = record
	}
	answered, declinedRecord := byOutcome[model.CallOutcomeAnswered], byOutcome[model.CallOutcomeDeclined]
	if answered == nil || declinedRecord == nil {
		t.Fatalf("call outcomes = %v, want one answered and one declined", byOutcome)
	}
	if declinedRecord.DurationSeconds != 0 {
		t.Errorf("declined call duration = %ds, want 0", declinedRecord.DurationSeconds)
	}
	if answered.DurationSeconds < 1 {
		t.Errorf("answered call duration = %ds, want at least 1", answered.DurationSeconds)
	}
	if answered.InitiatorID != caller {
		t.Errorf("call record initiator = %d, want %d", answered.InitiatorID, caller)
	}
}

// cleanupHTTPE2E removes a test conversation and the calls hanging off it.
func cleanupHTTPE2E(t *testing.T, conversationID int64) {
	t.Helper()
	t.Cleanup(func() {
		if _, err := database.DB.Exec("DELETE FROM call_sessions WHERE conversation_id = $1", conversationID); err != nil {
			t.Logf("cleanup: deleting calls failed: %v", err)
		}
		if _, err := database.DB.Exec("DELETE FROM conversations WHERE id = $1", conversationID); err != nil {
			t.Logf("cleanup: deleting conversation failed: %v", err)
		}
	})
}
