package repository

import (
	"database/sql"
	"fmt"

	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/model"
)

const (
	// RingTimeout is how many seconds a call may stay unanswered before the
	// sweeper cancels it. Clients use the same budget so the two ends agree on
	// when a missed call has really been missed.
	RingTimeout = 45

	// MaxSignalBytes caps a single opaque signalling payload (SDP or ICE
	// candidate). Postgres NOTIFY rejects payloads above 8000 bytes and the
	// event envelope adds ~200 more, so anything near that limit has to be
	// refused at the API instead of silently disappearing inside pg_notify.
	// Audio SDP is ~1 KB and a candidate is a few hundred bytes, so real traffic
	// is far below this; video SDP with simulcast is the closest case.
	MaxSignalBytes = 6000
)

// CallRepository persists call sessions and their signalling metadata. It never
// stores media or signalling payloads: a call's state machine and roster are the
// only server-side record (see docs/calls.md).
type CallRepository struct{}

// NewCallRepository creates a new CallRepository.
func NewCallRepository() *CallRepository {
	return &CallRepository{}
}

// CreateRinging opens a call and invites the given users. The initiator is
// recorded as an accepted participant, everyone else as invited.
//
// idx_calls_live (partial unique on conversation_id WHERE state <> 'ended')
// makes a second concurrent call in the same conversation fail with
// ErrCallAlreadyActive instead of ringing two phones at once.
func (r *CallRepository) CreateRinging(conversationID, initiatorID int64, mode string, invitees []int64) (*model.CallSession, error) {
	tx, err := database.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	call := &model.CallSession{}
	err = tx.QueryRow(
		`INSERT INTO call_sessions (conversation_id, initiator_id, mode)
		 VALUES ($1, $2, $3)
		 RETURNING id, conversation_id, initiator_id, mode, state, created_at`,
		conversationID, initiatorID, mode,
	).Scan(&call.ID, &call.ConversationID, &call.InitiatorID, &call.Mode, &call.State, &call.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrCallAlreadyActive
		}
		return nil, fmt.Errorf("failed to open call: %w", err)
	}

	if _, err := tx.Exec(
		`INSERT INTO call_participants (call_id, user_id, state, joined_at)
		 VALUES ($1, $2, 'accepted', NOW())
		 ON CONFLICT (call_id, user_id) DO NOTHING`,
		call.ID, initiatorID,
	); err != nil {
		return nil, fmt.Errorf("failed to add call initiator: %w", err)
	}

	for _, userID := range invitees {
		if userID == initiatorID || userID <= 0 {
			continue
		}
		if _, err := tx.Exec(
			`INSERT INTO call_participants (call_id, user_id, state)
			 VALUES ($1, $2, 'invited')
			 ON CONFLICT (call_id, user_id) DO NOTHING`,
			call.ID, userID,
		); err != nil {
			return nil, fmt.Errorf("failed to invite %d to call: %w", userID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	call.Participants, err = r.ListParticipants(call.ID)
	if err != nil {
		return nil, err
	}
	return call, nil
}

// Get loads one call with its roster. Duration is derived, not stored, so it is
// always consistent with the timestamps.
func (r *CallRepository) Get(callID int64) (*model.CallSession, error) {
	call := &model.CallSession{}
	var answered, ended sql.NullTime
	err := database.DB.QueryRow(
		`SELECT id, conversation_id, initiator_id, mode, state, end_reason, created_at,
		        answered_at, ended_at,
		        CASE WHEN answered_at IS NULL THEN 0
		             ELSE GREATEST(EXTRACT(EPOCH FROM (COALESCE(ended_at, NOW()) - answered_at))::bigint, 0)
		        END
		   FROM call_sessions
		  WHERE id = $1`,
		callID,
	).Scan(&call.ID, &call.ConversationID, &call.InitiatorID, &call.Mode, &call.State,
		&call.EndReason, &call.CreatedAt, &answered, &ended, &call.DurationSeconds)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load call: %w", err)
	}
	if answered.Valid {
		call.AnsweredAt = &answered.Time
	}
	if ended.Valid {
		call.EndedAt = &ended.Time
	}
	if call.Participants, err = r.ListParticipants(callID); err != nil {
		return nil, err
	}
	return call, nil
}

// GetLiveForConversation returns the ringing or active call of a conversation,
// or nil when there is none.
func (r *CallRepository) GetLiveForConversation(conversationID int64) (*model.CallSession, error) {
	var callID int64
	err := database.DB.QueryRow(
		`SELECT id FROM call_sessions
		  WHERE conversation_id = $1 AND state <> 'ended'
		  ORDER BY id DESC LIMIT 1`,
		conversationID,
	).Scan(&callID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to look up live call: %w", err)
	}
	return r.Get(callID)
}

// ListParticipants returns the roster of a call ordered by user id.
func (r *CallRepository) ListParticipants(callID int64) ([]model.CallParticipant, error) {
	rows, err := database.DB.Query(
		`SELECT call_id, user_id, state, joined_at, left_at
		   FROM call_participants
		  WHERE call_id = $1
		  ORDER BY user_id`,
		callID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list call participants: %w", err)
	}
	defer rows.Close()

	out := make([]model.CallParticipant, 0, 2)
	for rows.Next() {
		var p model.CallParticipant
		var joined, left sql.NullTime
		if err := rows.Scan(&p.CallID, &p.UserID, &p.State, &joined, &left); err != nil {
			return nil, fmt.Errorf("failed to scan call participant: %w", err)
		}
		if joined.Valid {
			p.JoinedAt = &joined.Time
		}
		if left.Valid {
			p.LeftAt = &left.Time
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ParticipantIDs returns the roster as plain user ids, for event recipients.
func (r *CallRepository) ParticipantIDs(callID int64) ([]int64, error) {
	participants, err := r.ListParticipants(callID)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(participants))
	for _, p := range participants {
		ids = append(ids, p.UserID)
	}
	return ids, nil
}

// IsParticipant reports whether the user was invited to the call.
func (r *CallRepository) IsParticipant(callID, userID int64) (bool, error) {
	var count int
	err := database.DB.QueryRow(
		`SELECT COUNT(*) FROM call_participants WHERE call_id = $1 AND user_id = $2`,
		callID, userID,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to check call participation: %w", err)
	}
	return count > 0, nil
}

// Answer moves a ringing call to active. It returns false when the call is no
// longer ringing — already answered on another device, declined, timed out or
// hung up — which the handler reports as 409 so a second device knows it lost
// the race rather than silently joining.
func (r *CallRepository) Answer(callID, userID int64) (bool, error) {
	tx, err := database.DB.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`UPDATE call_sessions SET state = 'active', answered_at = NOW()
		  WHERE id = $1 AND state = 'ringing'`,
		callID,
	)
	if err != nil {
		return false, fmt.Errorf("failed to answer call: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected == 0 {
		return false, nil
	}

	if _, err := tx.Exec(
		`UPDATE call_participants
		    SET state = 'accepted', joined_at = COALESCE(joined_at, NOW())
		  WHERE call_id = $1 AND user_id = $2`,
		callID, userID,
	); err != nil {
		return false, fmt.Errorf("failed to mark participant accepted: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// SetParticipantState updates one participant's state (declined, left).
func (r *CallRepository) SetParticipantState(callID, userID int64, state string) error {
	_, err := database.DB.Exec(
		`UPDATE call_participants
		    SET state = $3,
		        joined_at = CASE WHEN $3 = 'accepted' THEN COALESCE(joined_at, NOW()) ELSE joined_at END,
		        left_at   = CASE WHEN $3 IN ('declined','left') THEN NOW() ELSE left_at END
		  WHERE call_id = $1 AND user_id = $2`,
		callID, userID, state,
	)
	if err != nil {
		return fmt.Errorf("failed to update participant state: %w", err)
	}
	return nil
}

// End terminates a live call and records why. It returns false when the call was
// already ended, so callers publish the "call ended" event exactly once.
func (r *CallRepository) End(callID int64, reason string) (bool, error) {
	res, err := database.DB.Exec(
		`UPDATE call_sessions
		    SET state = 'ended', end_reason = $2, ended_at = NOW()
		  WHERE id = $1 AND state <> 'ended'`,
		callID, reason,
	)
	if err != nil {
		return false, fmt.Errorf("failed to end call: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// SweepRinging cancels calls that have been ringing longer than RingTimeout and
// returns them (with rosters) so the caller can publish "call ended: timeout".
// Without this, a caller who kills the app leaves the callee ringing forever and
// the conversation blocked by idx_calls_live.
func (r *CallRepository) SweepRinging() ([]model.CallSession, error) {
	rows, err := database.DB.Query(
		`UPDATE call_sessions
		    SET state = 'ended', end_reason = 'timeout', ended_at = NOW()
		  WHERE state = 'ringing' AND created_at < NOW() - ($1::int * INTERVAL '1 second')
		  RETURNING id`,
		RingTimeout,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to sweep ringing calls: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("failed to scan swept call: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	swept := make([]model.CallSession, 0, len(ids))
	for _, id := range ids {
		call, err := r.Get(id)
		if err != nil {
			return nil, err
		}
		swept = append(swept, *call)
	}
	return swept, nil
}
