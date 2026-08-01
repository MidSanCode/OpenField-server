package storage

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/openfield/server/internal/config"
	"github.com/openfield/server/internal/logger"
)

// Store wraps a RustFS (S3-compatible) client.
type Store struct {
	client        *minio.Client
	bucket        string
	publicBaseURL string
}

// New creates a new storage client from config.
func New(cfg config.StorageConfig) (*Store, error) {
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create storage client: %w", err)
	}

	s := &Store{
		client:        client,
		bucket:        cfg.Bucket,
		publicBaseURL: cfg.PublicBaseURL,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	exists, err := client.BucketExists(ctx, s.bucket)
	if err != nil {
		return nil, fmt.Errorf("failed to check bucket: %w", err)
	}
	if !exists {
		if err := client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{}); err != nil {
			return nil, fmt.Errorf("failed to create bucket: %w", err)
		}
		logger.Log.Info("storage bucket created", "bucket", s.bucket)
	}

	logger.Log.Info("storage initialized", "endpoint", cfg.Endpoint, "bucket", cfg.Bucket)
	return s, nil
}

// Upload stores a file and returns its object key and public URL.
func (s *Store) Upload(ctx context.Context, reader io.Reader, size int64, contentType, originalName string) (string, string, error) {
	ext := filepath.Ext(originalName)
	objectKey := fmt.Sprintf("%s/%s%s", time.Now().Format("2006/01/02"), uuid.NewString(), ext)

	_, err := s.client.PutObject(ctx, s.bucket, objectKey, reader, size, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return "", "", fmt.Errorf("failed to upload object: %w", err)
	}

	url := s.publicBaseURL + "/" + objectKey
	return objectKey, url, nil
}

// Delete removes an object from storage.
func (s *Store) Delete(ctx context.Context, objectKey string) error {
	err := s.client.RemoveObject(ctx, s.bucket, objectKey, minio.RemoveObjectOptions{})
	if err != nil {
		return fmt.Errorf("failed to delete object: %w", err)
	}
	return nil
}
