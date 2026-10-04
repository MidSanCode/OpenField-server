package repository

import (
	"database/sql"
	"time"

	"github.com/openfield/server/pkg/database"
)

// UploadSession tracks one in-progress chunked upload. Every chunk write,
// status poll and completion is bound to the owning user so one account can
// neither write chunks into nor complete another account's session.
type UploadSession struct {
	UploadID    string
	UserID      int64
	Bucket      string
	TotalChunks int
	SizeBytes   int64
	CreatedAt   time.Time
}

// CreateUploadSession persists a new chunked-upload session.
func CreateUploadSession(s *UploadSession) error {
	_, err := database.DB.Exec(
		"INSERT INTO upload_sessions (upload_id, user_id, bucket, total_chunks, size_bytes) "+
			"VALUES ($1, $2, $3, $4, $5)",
		s.UploadID, s.UserID, s.Bucket, s.TotalChunks, s.SizeBytes,
	)
	return err
}

// GetUploadSession returns the session with the given id, or (nil, nil) when
// it does not exist.
func GetUploadSession(uploadID string) (*UploadSession, error) {
	s := &UploadSession{}
	err := database.DB.QueryRow(
		"SELECT upload_id, user_id, bucket, total_chunks, size_bytes, created_at FROM upload_sessions WHERE upload_id = $1",
		uploadID,
	).Scan(&s.UploadID, &s.UserID, &s.Bucket, &s.TotalChunks, &s.SizeBytes, &s.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}

// DeleteUploadSession removes a finished or aborted session.
func DeleteUploadSession(uploadID string) error {
	_, err := database.DB.Exec("DELETE FROM upload_sessions WHERE upload_id = $1", uploadID)
	return err
}

// PurgeStaleUploadSessions removes sessions older than maxAge whose chunks were
// never completed. It returns the purged sessions so the caller can delete the
// chunk OBJECTS too: those never enter the attachments table, so they are
// invisible to quota accounting and to every cleanup path that works from the
// database. The previous version deleted only the rows and relied on bucket
// lifecycle rules that this repository never configures, so abandoned chunks
// accumulated forever.
func PurgeStaleUploadSessions(maxAge time.Duration) ([]UploadSession, error) {
	rows, err := database.DB.Query(
		`DELETE FROM upload_sessions
		  WHERE created_at < NOW() - ($1 || ' seconds')::interval
		  RETURNING upload_id, user_id, bucket, total_chunks, size_bytes, created_at`,
		int(maxAge.Seconds()),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	purged := make([]UploadSession, 0)
	for rows.Next() {
		var s UploadSession
		if err := rows.Scan(&s.UploadID, &s.UserID, &s.Bucket, &s.TotalChunks, &s.SizeBytes, &s.CreatedAt); err != nil {
			return nil, err
		}
		purged = append(purged, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return purged, nil
}
