package model

import "time"

// Call lifecycle states. A call is in exactly one of them; the state machine is
//
//	ringing ──answer──► active ──hangup──► ended
//	   │
//	   └──decline|cancel|timeout──────────► ended
//
// "ended" is terminal: a finished call is never reopened, a new one is created
// instead. Keeping that invariant is what lets call_sessions carry a partial
// unique index enforcing one live call per conversation.
const (
	CallStateRinging = "ringing"
	CallStateActive  = "active"
	CallStateEnded   = "ended"
)

// Call modes. Only "audio" is produced by clients today; "video" is accepted by
// the API and stored so the signalling contract does not need a breaking change
// when video lands.
const (
	CallModeAudio = "audio"
	CallModeVideo = "video"
)

// Call participant states.
const (
	CallParticipantInvited  = "invited"
	CallParticipantAccepted = "accepted"
	CallParticipantDeclined = "declined"
	CallParticipantLeft     = "left"
)

// CallSession is one voice/video call placed inside a conversation.
//
// The server never sees media: this row plus the signalling relay is the whole
// server-side footprint of a peer-to-peer call (see docs/calls.md).
type CallSession struct {
	ID             int64  `json:"id"`
	ConversationID int64  `json:"conversation_id"`
	InitiatorID    int64  `json:"initiator_id"`
	Mode           string `json:"mode"`  // audio | video
	State          string `json:"state"` // ringing | active | ended
	// EndReason is set when the call reaches "ended": hung_up, cancelled,
	// declined, timeout or replaced. Empty while the call is live.
	EndReason  string     `json:"end_reason,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	AnsweredAt *time.Time `json:"answered_at,omitempty"`
	EndedAt    *time.Time `json:"ended_at,omitempty"`
	// DurationSeconds is derived, never stored: 0 until the call is answered,
	// then the elapsed time up to the end of the call (or now, while live).
	DurationSeconds int64 `json:"duration_seconds"`
	// Participants is the roster, populated by the lookups that need it.
	Participants []CallParticipant `json:"participants,omitempty"`
}

// CallParticipant is one invited party of a call.
type CallParticipant struct {
	CallID   int64      `json:"call_id"`
	UserID   int64      `json:"user_id"`
	State    string     `json:"state"` // invited | accepted | declined | left
	JoinedAt *time.Time `json:"joined_at,omitempty"`
	LeftAt   *time.Time `json:"left_at,omitempty"`
}

// CallRecordKind is the message kind used for call history entries.
//
// Unlike the system.* kinds, a call entry carries data a client cannot derive
// from the kind alone (outcome, duration), so it keeps a structured payload
// beside it — see CallRecord.
const CallRecordKind = "call"

// Call outcomes stored on a call history message.
const (
	// CallOutcomeAnswered means the callee picked up.
	CallOutcomeAnswered = "answered"
	// CallOutcomeDeclined means the callee rejected the invitation.
	CallOutcomeDeclined = "declined"
	// CallOutcomeMissed means the invitation rang out without an answer.
	CallOutcomeMissed = "missed"
	// CallOutcomeCancelled means the caller gave up while it was still ringing.
	CallOutcomeCancelled = "cancelled"
)

// CallRecord is the structured payload of a call history message (kind "call").
//
// It exists so the wording lives in the client's translation files rather than
// in a sentence the server composes: both parties see the same row, but "Missed
// call" reads differently depending on who is looking at it.
type CallRecord struct {
	CallID          int64  `json:"call_id"`
	Mode            string `json:"mode"`    // audio | video
	Outcome         string `json:"outcome"` // one of the CallOutcome constants
	DurationSeconds int    `json:"duration_seconds"`
	InitiatorID     int64  `json:"initiator_id"`
}
