package repository

import (
	"errors"
	"fmt"
	"testing"

	"github.com/openfield/server/pkg/database"
)

// TestCallLifecycleE2E covers the call state machine against a real database:
// the lifecycle itself, the one-live-call-per-conversation invariant (a partial
// unique index — the only thing standing between two dials and a double ring),
// the answer race, and that ending a call frees the conversation again.
//
// Gated like the other repository e2e tests:
//
//	OF_REPO_E2E=1 OF_REPO_E2E_DB=of_scratch go test ./pkg/repository/
func TestCallLifecycleE2E(t *testing.T) {
	connectRepoE2E(t)
	callRepo := NewCallRepository()
	convRepo := NewConversationRepository()

	caller := makeE2EUser(t, "e2e_call_caller")
	callee := makeE2EUser(t, "e2e_call_callee")

	conv, err := convRepo.CreatePrivate(caller, callee, false)
	if err != nil {
		t.Fatalf("creating the private conversation failed: %v", err)
	}
	cleanupCallE2E(t, conv.ID)

	// The sweeper is only cheap if its index is really there; a missing index
	// would not fail any assertion below, so check the migrations applied.
	for _, name := range []string{"idx_calls_live", "idx_calls_ringing"} {
		if n := countRows(t, `SELECT COUNT(*) FROM pg_indexes WHERE indexname = $1`, name); n != 1 {
			t.Fatalf("%s is missing (found %d): the call migrations did not apply", name, n)
		}
	}

	// ---- open a call ----
	call, err := callRepo.CreateRinging(conv.ID, caller, "audio", []int64{callee})
	if err != nil {
		t.Fatalf("opening the call failed: %v", err)
	}
	if call.State != "ringing" || call.Mode != "audio" {
		t.Fatalf("new call = %+v, want ringing/audio", call)
	}
	if len(call.Participants) != 2 {
		t.Fatalf("participant count = %d, want 2", len(call.Participants))
	}
	for _, p := range call.Participants {
		switch p.UserID {
		case caller:
			if p.State != "accepted" {
				t.Errorf("initiator state = %q, want accepted", p.State)
			}
			if p.JoinedAt == nil {
				t.Error("initiator should have joined_at set")
			}
		case callee:
			if p.State != "invited" {
				t.Errorf("callee state = %q, want invited", p.State)
			}
			if p.JoinedAt != nil {
				t.Error("callee must not have joined_at before answering")
			}
		default:
			t.Errorf("unexpected participant %d", p.UserID)
		}
	}

	// ---- one live call per conversation ----
	if _, err := callRepo.CreateRinging(conv.ID, caller, "audio", []int64{callee}); !errors.Is(err, ErrCallAlreadyActive) {
		t.Fatalf("second call while ringing: err = %v, want ErrCallAlreadyActive", err)
	}

	// The conversation lookup clients use to recover state must see it.
	live, err := callRepo.GetLiveForConversation(conv.ID)
	if err != nil {
		t.Fatalf("GetLiveForConversation failed: %v", err)
	}
	if live == nil || live.ID != call.ID {
		t.Fatalf("live call = %+v, want id %d", live, call.ID)
	}

	// ---- answering, and losing the race ----
	if answered, err := callRepo.Answer(call.ID, callee); err != nil {
		t.Fatalf("Answer failed: %v", err)
	} else if !answered {
		t.Fatal("the callee's first Answer should win")
	}
	if answered, err := callRepo.Answer(call.ID, callee); err != nil {
		t.Fatalf("second Answer failed: %v", err)
	} else if answered {
		t.Fatal("a second Answer must lose: the call is no longer ringing")
	}

	active, err := callRepo.Get(call.ID)
	if err != nil {
		t.Fatalf("Get after answer failed: %v", err)
	}
	if active.State != "active" {
		t.Errorf("state = %q, want active", active.State)
	}
	if active.AnsweredAt == nil {
		t.Error("answered_at is not set on an active call")
	}
	if active.DurationSeconds < 0 {
		t.Errorf("duration = %d, want >= 0", active.DurationSeconds)
	}
	if active.EndedAt != nil {
		t.Error("ended_at must be empty on an active call")
	}
	// An active call still occupies the conversation.
	if _, err := callRepo.CreateRinging(conv.ID, caller, "audio", []int64{callee}); !errors.Is(err, ErrCallAlreadyActive) {
		t.Errorf("call while active: err = %v, want ErrCallAlreadyActive", err)
	}

	// ---- ending is idempotent and frees the conversation ----
	if ended, err := callRepo.End(call.ID, "hung_up"); err != nil {
		t.Fatalf("End failed: %v", err)
	} else if !ended {
		t.Fatal("the first End should report that it ended the call")
	}
	if ended, err := callRepo.End(call.ID, "hung_up"); err != nil {
		t.Fatalf("second End failed: %v", err)
	} else if ended {
		t.Fatal("the second End must report false so call.ended is published once")
	}

	finished, err := callRepo.Get(call.ID)
	if err != nil {
		t.Fatalf("Get after end failed: %v", err)
	}
	if finished.State != "ended" || finished.EndReason != "hung_up" {
		t.Errorf("call = %q/%q, want ended/hung_up", finished.State, finished.EndReason)
	}
	if finished.EndedAt == nil {
		t.Error("ended_at is not set on an ended call")
	}
	if live, err := callRepo.GetLiveForConversation(conv.ID); err != nil {
		t.Fatalf("GetLiveForConversation after end failed: %v", err)
	} else if live != nil {
		t.Fatalf("an ended call should not be live, got %+v", live)
	}

	// A new call may now be placed in the same conversation.
	second, err := callRepo.CreateRinging(conv.ID, callee, "video", []int64{caller})
	if err != nil {
		t.Fatalf("placing a call after the previous one ended failed: %v", err)
	}
	if second.ID == call.ID {
		t.Error("the new call reused the old id")
	}
	if _, err := callRepo.End(second.ID, "cancelled"); err != nil {
		t.Fatalf("ending the second call failed: %v", err)
	}
}

// TestCallSweepE2E proves the sweeper reclaims a call nobody answered — what
// stops an abandoned dial from ringing forever and blocking its conversation —
// while leaving calls that are still inside the ring window alone.
func TestCallSweepE2E(t *testing.T) {
	connectRepoE2E(t)
	callRepo := NewCallRepository()
	convRepo := NewConversationRepository()

	callerA := makeE2EUser(t, "e2e_sweep_caller")
	calleeA := makeE2EUser(t, "e2e_sweep_callee")
	convA, err := convRepo.CreatePrivate(callerA, calleeA, false)
	if err != nil {
		t.Fatalf("creating the first conversation failed: %v", err)
	}
	cleanupCallE2E(t, convA.ID)

	callerB := makeE2EUser(t, "e2e_sweep_caller2")
	calleeB := makeE2EUser(t, "e2e_sweep_callee2")
	convB, err := convRepo.CreatePrivate(callerB, calleeB, false)
	if err != nil {
		t.Fatalf("creating the second conversation failed: %v", err)
	}
	cleanupCallE2E(t, convB.ID)

	stale, err := callRepo.CreateRinging(convA.ID, callerA, "audio", []int64{calleeA})
	if err != nil {
		t.Fatalf("opening the stale call failed: %v", err)
	}
	// A call that has been ringing past the timeout. Backdating created_at is
	// the only way to test this without sleeping for RingTimeout.
	if _, err := database.DB.Exec(
		fmt.Sprintf("UPDATE call_sessions SET created_at = NOW() - INTERVAL '%d seconds' WHERE id = $1", RingTimeout+10),
		stale.ID,
	); err != nil {
		t.Fatalf("backdating the call failed: %v", err)
	}

	// A call in another conversation that is still inside the ring window.
	fresh, err := callRepo.CreateRinging(convB.ID, callerB, "audio", []int64{calleeB})
	if err != nil {
		t.Fatalf("opening the fresh call failed: %v", err)
	}

	swept, err := callRepo.SweepRinging()
	if err != nil {
		t.Fatalf("SweepRinging failed: %v", err)
	}
	var found *struct{ participants int }
	for i := range swept {
		if swept[i].ID != stale.ID {
			if swept[i].ID == fresh.ID {
				t.Fatalf("the sweeper cancelled a call that is still ringing (id %d)", fresh.ID)
			}
			continue
		}
		found = &struct{ participants int }{participants: len(swept[i].Participants)}
		if swept[i].EndReason != "timeout" {
			t.Errorf("swept call reason = %q, want timeout", swept[i].EndReason)
		}
	}
	if found == nil {
		t.Fatal("the stale ringing call was not swept")
	}
	if found.participants != 2 {
		t.Errorf("swept call carries %d participants, want 2 so call.ended has recipients", found.participants)
	}

	got, err := callRepo.Get(stale.ID)
	if err != nil {
		t.Fatalf("Get after sweep failed: %v", err)
	}
	if got.State != "ended" {
		t.Errorf("swept call state = %q, want ended", got.State)
	}

	// The fresh call is untouched and still live.
	stillLive, err := callRepo.GetLiveForConversation(convB.ID)
	if err != nil {
		t.Fatalf("GetLiveForConversation failed: %v", err)
	}
	if stillLive == nil || stillLive.ID != fresh.ID {
		t.Fatalf("the untouched call = %+v, want id %d", stillLive, fresh.ID)
	}
	if _, err := callRepo.End(fresh.ID, "cancelled"); err != nil {
		t.Fatalf("ending the fresh call failed: %v", err)
	}

	// With the stale call reclaimed, its conversation can be dialled again.
	recovered, err := callRepo.CreateRinging(convA.ID, callerA, "audio", []int64{calleeA})
	if err != nil {
		t.Fatalf("placing a call after the sweep failed: %v", err)
	}
	if _, err := callRepo.End(recovered.ID, "cancelled"); err != nil {
		t.Fatalf("ending the recovered call failed: %v", err)
	}
}

// cleanupCallE2E removes a test conversation and the calls hanging off it.
func cleanupCallE2E(t *testing.T, conversationID int64) {
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
