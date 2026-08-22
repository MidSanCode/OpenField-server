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
// never completed. The orphaned chunk objects are cleaned by the storage
// lifecycle rules; this only keeps the table tidy and frees the quota slot.
func PurgeStaleUploadSessions(maxAge time.Duration) error {
	_, err := database.DB.Exec(
		"DELETE FROM upload_sessions WHERE created_at < NOW() - ($1 || ' seconds')::interval",
		int(maxAge.Seconds()),
	)
	return err
}
