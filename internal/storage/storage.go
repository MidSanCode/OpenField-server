package storage

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/openfield/server/internal/config"
	"github.com/openfield/server/internal/logger"
)

// normalizeEndpoint strips any path from the endpoint so S3 clients only see a
// scheme://host[:port]. Configs that end with a "/" or carry a path prefix
// (e.g. "https://io.msc-studio.eu.cc/") otherwise make minio-go reject the URL
// as "Endpoint url cannot have fully qualified paths." and storage silently
// stays disabled.
func normalizeEndpoint(endpoint string) string {
	endpoint = strings.TrimRight(endpoint, "/")
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return endpoint
	}
	u.Path = ""
	u.RawPath = ""
	return strings.TrimRight(u.String(), "/")
}

// Store wraps a RustFS (S3-compatible) client.
type Store struct {
	client        *minio.Client
	bucket        string
	publicBaseURL string
	enabled       bool
}

// IsConfigured reports whether object storage has been configured.
func IsConfigured(cfg config.StorageConfig) bool {
	return cfg.Endpoint != "" && cfg.Bucket != ""
}

// Enabled reports whether this store has a backing object store.
func (s *Store) Enabled() bool {
	return s != nil && s.enabled
}

// New creates a new storage client from config. When object storage is not
// configured it returns a disabled store (never an error), so services can
// still start without storage. The bucket existence check is best-effort:
// an unreachable or not-yet-created bucket never fails startup — operations
// fail at request time and recover once the storage backend is available.
func New(cfg config.StorageConfig) (*Store, error) {
	if !IsConfigured(cfg) {
		logger.Log.Warn("storage not configured; running without object storage")
		return &Store{enabled: false}, nil
	}

	client, err := minio.New(normalizeEndpoint(cfg.Endpoint), &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		// A malformed endpoint must not take the service down either.
		logger.Log.Error("failed to create storage client; running without object storage", "error", err)
		return &Store{enabled: false}, nil
	}

	s := &Store{
		client:        client,
		bucket:        cfg.Bucket,
		publicBaseURL: cfg.PublicBaseURL,
		enabled:       true,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	exists, err := client.BucketExists(ctx, s.bucket)
	if err != nil {
		logger.Log.Warn("storage bucket unavailable at startup", "bucket", s.bucket, "error", err)
		return s, nil
	}
	if !exists {
		if err := client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{}); err != nil {
			logger.Log.Warn("failed to create storage bucket", "bucket", s.bucket, "error", err)
			return s, nil
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
