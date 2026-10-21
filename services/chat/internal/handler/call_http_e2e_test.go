package handler

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
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

// rosterStates flattens a decoded call's participants into user id -> state.
func rosterStates(call map[string]any) map[int64]string {
	out := map[int64]string{}
	raw, _ := call["participants"].([]any)
	for _, item := range raw {
		p, _ := item.(map[string]any)
		uid, _ := p["user_id"].(float64)
		state, _ := p["state"].(string)
		out[int64(uid)] = state
	}
	return out
}

// TestGroupCallHTTPE2E locks the mesh contract: a group call rings every active
// non-bot member, each member joins and leaves independently, signalling is
// targetable per pair, and the call ends only when no accepted participant is
// left.
func TestGroupCallHTTPE2E(t *testing.T) {
	connectHandlerE2E(t)
	gin.SetMode(gin.TestMode)

	convRepo := repository.NewConversationRepository()
	caller := newHandlerE2EUser(t, "e2e_gcaller", false)
	g1 := newHandlerE2EUser(t, "e2e_g1", false)
	g2 := newHandlerE2EUser(t, "e2e_g2", false)
	g3 := newHandlerE2EUser(t, "e2e_g3", false)
	gbot := newHandlerE2EUser(t, "e2e_gbot", true)
	outsider := newHandlerE2EUser(t, "e2e_gout", false)

	group, err := convRepo.CreateGroup(caller, fmt.Sprintf("e2e_group_call_%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("creating the group conversation failed: %v", err)
	}
	cleanupHTTPE2E(t, group.ID)
	for _, m := range []int64{g1, g2, g3, gbot} {
		if err := convRepo.AddMember(group.ID, m, caller, "member", "active"); err != nil {
			t.Fatalf("adding member %d failed: %v", m, err)
		}
	}

	r := gin.New()
	r.Use(middleware.Recovery())
	RegisterRoutes(r, NewConversationHandler(), NewConsentHandler(), NewMessageHandler(nil),
		NewGroupExtrasHandler(nil), NewCallHandler(nil))

	// ---- open a group call ----
	w := callRequest(t, r, http.MethodPost, "/api/v1/calls", caller,
		map[string]any{"conversation_id": group.ID, "mode": "video"})
	if w.Code != http.StatusCreated {
		t.Fatalf("opening a group call: status = %d, want 201 (body %s)", w.Code, w.Body.String())
	}
	opened := decodeCall(t, w)
	callID := int64(opened["id"].(float64))
	if opened["state"] != "ringing" || opened["mode"] != "video" {
		t.Fatalf("opened group call = %v, want ringing/video", opened)
	}
	roster := rosterStates(opened)
	if roster[caller] != "accepted" || roster[g1] != "invited" || roster[g2] != "invited" || roster[g3] != "invited" {
		t.Fatalf("group roster = %v, want caller accepted and members invited", roster)
	}
	// The bot is on the membership list but must never be rung: it has no media
	// endpoint, so ringing it would just produce a phone that never picks up.
	if _, rung := roster[gbot]; rung {
		t.Errorf("bot was invited to a call: roster = %v", roster)
	}

	// ---- second dial refused; non-member refused ----
	if w := callRequest(t, r, http.MethodPost, "/api/v1/calls", caller,
		map[string]any{"conversation_id": group.ID}); w.Code != http.StatusConflict {
		t.Errorf("second group dial: status = %d, want 409", w.Code)
	}
	if w := callRequest(t, r, http.MethodPost, "/api/v1/calls", outsider,
		map[string]any{"conversation_id": group.ID}); w.Code != http.StatusForbidden {
		t.Errorf("non-member group dial: status = %d, want 403", w.Code)
	}

	// ---- each member answers for themselves ----
	w = callRequest(t, r, http.MethodPost, fmt.Sprintf("/api/v1/calls/%d/answer", callID), g1, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("first group answer: status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if answered := decodeCall(t, w); answered["state"] != "active" {
		t.Fatalf("first answer should activate the call, state = %v", answered["state"])
	}
	// Joining an already-active call is normal in a mesh — no lost-race 409.
	w = callRequest(t, r, http.MethodPost, fmt.Sprintf("/api/v1/calls/%d/answer", callID), g2, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("joining an active group call: status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if joined := rosterStates(decodeCall(t, w)); joined[g1] != "accepted" || joined[g2] != "accepted" {
		t.Errorf("roster after two joins = %v, want g1/g2 accepted", joined)
	}

	// ---- targeted signalling: one pair at a time ----
	if w := callRequest(t, r, http.MethodPost, fmt.Sprintf("/api/v1/calls/%d/signal", callID), caller,
		map[string]any{"type": "offer", "to_user_id": g1, "payload": map[string]any{"sdp": "v=0..."}}); w.Code != http.StatusOK {
		t.Errorf("targeted offer: status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if w := callRequest(t, r, http.MethodPost, fmt.Sprintf("/api/v1/calls/%d/signal", callID), caller,
		map[string]any{"type": "candidate", "to_user_id": g2, "payload": map[string]any{"candidate": "candidate:..."}}); w.Code != http.StatusOK {
		t.Errorf("targeted candidate: status = %d, want 200", w.Code)
	}
	// Broadcast signalling still works (the one-to-one client's contract).
	if w := callRequest(t, r, http.MethodPost, fmt.Sprintf("/api/v1/calls/%d/signal", callID), g2,
		map[string]any{"type": "answer", "payload": map[string]any{"sdp": "v=0..."}}); w.Code != http.StatusOK {
		t.Errorf("broadcast answer: status = %d, want 200", w.Code)
	}
	if w := callRequest(t, r, http.MethodPost, fmt.Sprintf("/api/v1/calls/%d/signal", callID), caller,
		map[string]any{"type": "offer", "to_user_id": outsider, "payload": map[string]any{"sdp": "v=0..."}}); w.Code != http.StatusForbidden {
		t.Errorf("signal to a non-participant: status = %d, want 403", w.Code)
	}
	if w := callRequest(t, r, http.MethodPost, fmt.Sprintf("/api/v1/calls/%d/signal", callID), caller,
		map[string]any{"type": "offer", "to_user_id": caller, "payload": map[string]any{"sdp": "v=0..."}}); w.Code != http.StatusBadRequest {
		t.Errorf("signal to self: status = %d, want 400", w.Code)
	}

	// ---- a decline only removes one ringer, the call goes on ----
	if w := callRequest(t, r, http.MethodPost, fmt.Sprintf("/api/v1/calls/%d/decline", callID), g3, nil); w.Code != http.StatusOK {
		t.Fatalf("group decline: status = %d, want 200", w.Code)
	}
	w = callRequest(t, r, http.MethodGet, fmt.Sprintf("/api/v1/calls/%d", callID), g1, nil)
	if still := decodeCall(t, w); still["state"] != "active" {
		t.Fatalf("call after one decline = %v, want still active", still["state"])
	}

	// ---- hanging up leaves the call running until nobody is left ----
	if w := callRequest(t, r, http.MethodPost, fmt.Sprintf("/api/v1/calls/%d/hangup", callID), g1, nil); w.Code != http.StatusOK {
		t.Fatalf("g1 hangup: status = %d, want 200", w.Code)
	}
	w = callRequest(t, r, http.MethodGet, fmt.Sprintf("/api/v1/calls/%d", callID), g2, nil)
	if after := decodeCall(t, w); after["state"] != "active" {
		t.Fatalf("call after g1 left = %v, want still active", after["state"])
	}
	if after := rosterStates(decodeCall(t, w)); after[g1] != "left" || after[g2] != "accepted" || after[caller] != "accepted" {
		t.Errorf("roster after g1 left = %v", after)
	}
	// g2 leaves: the caller is still accepted, so the call continues.
	if w := callRequest(t, r, http.MethodPost, fmt.Sprintf("/api/v1/calls/%d/hangup", callID), g2, nil); w.Code != http.StatusOK {
		t.Fatalf("g2 hangup: status = %d, want 200", w.Code)
	}
	w = callRequest(t, r, http.MethodGet, fmt.Sprintf("/api/v1/calls/%d", callID), caller, nil)
	if still := decodeCall(t, w); still["state"] != "active" {
		t.Fatalf("call after g2 left = %v, want still active (caller is connected)", still["state"])
	}
	// The caller leaving empties the accepted set, which ends the call.
	if w := callRequest(t, r, http.MethodPost, fmt.Sprintf("/api/v1/calls/%d/hangup", callID), caller, nil); w.Code != http.StatusOK {
		t.Fatalf("caller hangup: status = %d, want 200", w.Code)
	}
	w = callRequest(t, r, http.MethodGet, fmt.Sprintf("/api/v1/calls/%d", callID), g2, nil)
	if ended := decodeCall(t, w); ended["state"] != "ended" || ended["end_reason"] != "hung_up" {
		t.Errorf("final group call = %v/%v, want ended/hung_up", ended["state"], ended["end_reason"])
	}

	// ---- ringing: declines keep it ringing, initiator cancel ends it ----
	w = callRequest(t, r, http.MethodPost, "/api/v1/calls", caller, map[string]any{"conversation_id": group.ID})
	if w.Code != http.StatusCreated {
		t.Fatalf("second group dial: status = %d, want 201", w.Code)
	}
	ringingID := int64(decodeCall(t, w)["id"].(float64))
	if w := callRequest(t, r, http.MethodPost, fmt.Sprintf("/api/v1/calls/%d/decline", ringingID), g1, nil); w.Code != http.StatusOK {
		t.Fatalf("ringing decline: status = %d, want 200", w.Code)
	}
	w = callRequest(t, r, http.MethodGet, fmt.Sprintf("/api/v1/calls/%d", ringingID), g2, nil)
	if still := decodeCall(t, w); still["state"] != "ringing" {
		t.Fatalf("call after one decline while ringing = %v, want still ringing", still["state"])
	}
	if w := callRequest(t, r, http.MethodPost, fmt.Sprintf("/api/v1/calls/%d/hangup", ringingID), caller, nil); w.Code != http.StatusOK {
		t.Fatalf("initiator cancel: status = %d, want 200", w.Code)
	}
	w = callRequest(t, r, http.MethodGet, fmt.Sprintf("/api/v1/calls/%d", ringingID), g2, nil)
	if cancelled := decodeCall(t, w); cancelled["state"] != "ended" || cancelled["end_reason"] != "cancelled" {
		t.Errorf("cancelled group call = %v/%v, want ended/cancelled", cancelled["state"], cancelled["end_reason"])
	}
}

// TestCallICECredentialsE2E locks the per-user TURN credential contract: with a
// turn_secret configured, GET /calls/ice returns a username carrying the user id
// and an expiry, plus an HMAC-SHA1 credential in coturn's static-auth-secret
// format — so the relay can authenticate the pair and the server can mint one
// per user without storing anything.
func TestCallICECredentialsE2E(t *testing.T) {
	connectHandlerE2E(t)
	gin.SetMode(gin.TestMode)

	caller := newHandlerE2EUser(t, "e2e_ice_user", false)
	r := gin.New()
	r.Use(middleware.Recovery())
	RegisterRoutes(r, NewConversationHandler(), NewConsentHandler(), NewMessageHandler(nil),
		NewGroupExtrasHandler(nil), NewCallHandler(&config.Config{Call: config.CallConfig{
			TURNURL:     "turn:127.0.0.1:3478",
			TURNSecret:  "test-secret-that-is-long-enough",
			STUNURLs:    []string{"stun:stun.example.com:3478"},
			TURNUsername: "static-user",
			TURNCredential: "static-pass",
		}}))

	// Per-user mode: the static pair is ignored.
	w := callRequest(t, r, http.MethodGet, "/api/v1/calls/ice", caller, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("ICE servers: status = %d, want 200", w.Code)
	}
	var ice struct {
		IceServers []map[string]string `json:"ice_servers"`
		Turn       bool                `json:"turn"`
		TTL        int                 `json:"ttl"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &ice); err != nil {
		t.Fatalf("decoding ICE response %q failed: %v", w.Body.String(), err)
	}
	if !ice.Turn {
		t.Fatal("turn_secret configured but turn = false")
	}
	if ice.TTL <= 0 {
		t.Errorf("ttl = %d, want positive", ice.TTL)
	}
	if len(ice.IceServers) == 0 || ice.IceServers[0]["urls"] == "" {
		t.Fatalf("ice_servers = %v, want at least one", ice.IceServers)
	}
	turn := turnEntry(ice.IceServers)
	if turn["urls"] == "" {
		t.Fatalf("ice_servers = %v, want a TURN entry", ice.IceServers)
	}
	username := turn["username"]
	credential := turn["credential"]
	if username == "" || credential == "" {
		t.Fatalf("per-user credential missing: username=%q credential=%q", username, credential)
	}
	// Username is <expiry-unix>:<user_id>.
	parts := strings.Split(username, ":")
	if len(parts) != 2 {
		t.Fatalf("username %q, want <expiry>:<user_id>", username)
	}
	if parts[1] != fmt.Sprintf("%d", caller) {
		t.Errorf("username carries user id %q, want %d", parts[1], caller)
	}
	expiry, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		t.Fatalf("expiry %q is not a unix timestamp: %v", parts[0], err)
	}
	if expiry < time.Now().Unix() || expiry > time.Now().Add(config.TurnTTL+time.Minute).Unix() {
		t.Errorf("expiry %d outside the TTL window", expiry)
	}
	// Credential is base64(HMAC-SHA1(secret, username)).
	mac := hmac.New(sha1.New, []byte("test-secret-that-is-long-enough"))
	mac.Write([]byte(username))
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if credential != want {
		t.Errorf("credential = %q, want %q", credential, want)
	}
	// A different user gets a different credential (user-scoped).
	w2 := callRequest(t, r, http.MethodGet, "/api/v1/calls/ice", 999999, nil)
	if w2.Code != http.StatusOK {
		t.Fatalf("ICE servers for another user: status = %d, want 200", w2.Code)
	}
	var ice2 struct {
		IceServers []map[string]string `json:"ice_servers"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &ice2); err != nil {
		t.Fatalf("decoding second ICE response failed: %v", err)
	}
	if turnEntry(ice2.IceServers)["username"] == username {
		t.Errorf("two users got the same TURN username %q (first %q)", turnEntry(ice2.IceServers)["username"], username)
	}
}

// turnEntry returns the TURN entry of an ICE server list, or an empty map.
func turnEntry(servers []map[string]string) map[string]string {
	for _, s := range servers {
		if strings.HasPrefix(s["urls"], "turn:") || strings.HasPrefix(s["urls"], "turns:") {
			return s
		}
	}
	return map[string]string{}
}

// TestCallICEStaticFallbackE2E: without a turn_secret the operator's static
// pair is served verbatim and no ttl is advertised, which is the contract
// existing deployments rely on.
func TestCallICEStaticFallbackE2E(t *testing.T) {
	connectHandlerE2E(t)
	gin.SetMode(gin.TestMode)

	caller := newHandlerE2EUser(t, "e2e_ice_static", false)
	r := gin.New()
	r.Use(middleware.Recovery())
	RegisterRoutes(r, NewConversationHandler(), NewConsentHandler(), NewMessageHandler(nil),
		NewGroupExtrasHandler(nil), NewCallHandler(&config.Config{Call: config.CallConfig{
			TURNURL:        "turn:127.0.0.1:3478",
			TURNUsername:   "static-user",
			TURNCredential: "static-pass",
		}}))

	w := callRequest(t, r, http.MethodGet, "/api/v1/calls/ice", caller, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("ICE servers: status = %d, want 200", w.Code)
	}
	var ice struct {
		IceServers []map[string]string `json:"ice_servers"`
		Turn       bool                `json:"turn"`
		TTL        int                 `json:"ttl"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &ice); err != nil {
		t.Fatalf("decoding ICE response failed: %v", err)
	}
	if !ice.Turn {
		t.Fatal("static TURN configured but turn = false")
	}
	if ice.TTL != 0 {
		t.Errorf("static mode ttl = %d, want 0 (not advertised)", ice.TTL)
	}
	turn := turnEntry(ice.IceServers)
	if turn["username"] != "static-user" || turn["credential"] != "static-pass" {
		t.Errorf("static credential = %v, want the configured pair", turn)
	}
}
