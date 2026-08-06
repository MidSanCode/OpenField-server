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
	"github.com/openfield/server/pkg/config"
	"github.com/openfield/server/pkg/logger"
)

// Store wraps an S3-compatible object store client (MinIO, AWS S3, RustFS, ...).
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
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create storage client: %w", err)
	}

	s := &Store{
		client:        client,
		bucket:        cfg.Bucket,
		publicBaseURL: resolvePublicBaseURL(cfg),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	exists, err := client.BucketExists(ctx, s.bucket)
	if err != nil {
		return nil, fmt.Errorf("failed to check bucket: %w", err)
	}
	if !exists {
		if err := client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{Region: cfg.Region}); err != nil {
			return nil, fmt.Errorf("failed to create bucket: %w", err)
		}
		logger.Log.Info("storage bucket created", "bucket", s.bucket)
	}

	logger.Log.Info("storage initialized", "endpoint", cfg.Endpoint, "bucket", cfg.Bucket)
	return s, nil
}

// resolvePublicBaseURL derives the base URL used to build public object URLs.
// An explicitly configured PublicBaseURL always wins; otherwise it is derived
// from the endpoint so AWS S3 and S3-compatible stores work out of the box.
func resolvePublicBaseURL(cfg config.StorageConfig) string {
	if cfg.PublicBaseURL != "" {
		return strings.TrimRight(cfg.PublicBaseURL, "/")
	}
	scheme := "http"
	if cfg.UseSSL {
		scheme = "https"
	}
	if strings.Contains(cfg.Endpoint, "amazonaws.com") {
		region := cfg.Region
		if region == "" {
			region = "us-east-1"
		}
		return fmt.Sprintf("https://%s.s3.%s.amazonaws.com", cfg.Bucket, region)
	}
	return fmt.Sprintf("%s://%s/%s", scheme, cfg.Endpoint, cfg.Bucket)
}

// publicURL builds the full URL for an object key against the resolved base.
// The base may already contain a bucket path (e.g. http://host/openfield), so
// append the key as additional path segments, keeping "/" as separators.
func (s *Store) publicURL(objectKey string) string {
	base, err := url.Parse(s.publicBaseURL)
	if err != nil {
		return s.publicBaseURL + "/" + objectKey
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/" + objectKey
	return base.String()
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

	return objectKey, s.publicURL(objectKey), nil
}

// UploadThumb stores a thumbnail image next to an object and returns its public URL.
// The thumbnail key is derived from the parent object key with a ".thumb.jpg" suffix.
func (s *Store) UploadThumb(ctx context.Context, parentObjectKey string, reader io.Reader, size int64) (string, error) {
	thumbKey := parentObjectKey + ".thumb.jpg"
	_, err := s.client.PutObject(ctx, s.bucket, thumbKey, reader, size, minio.PutObjectOptions{
		ContentType: "image/jpeg",
	})
	if err != nil {
		return "", fmt.Errorf("failed to upload thumbnail: %w", err)
	}
	return s.publicURL(thumbKey), nil
}

// GetBytes downloads the full contents of an object into memory. It is meant
// for small objects only (e.g. thumbnail generation after chunk assembly).
func (s *Store) GetBytes(ctx context.Context, objectKey string, max int64) ([]byte, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, objectKey, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to open object: %w", err)
	}
	defer obj.Close()
	stat, err := obj.Stat()
	if err != nil {
		return nil, fmt.Errorf("failed to stat object: %w", err)
	}
	if stat.Size > max {
		return nil, fmt.Errorf("object too large: %d > %d", stat.Size, max)
	}
	return io.ReadAll(obj)
}

// Delete removes an object from storage.
func (s *Store) Delete(ctx context.Context, objectKey string) error {
	err := s.client.RemoveObject(ctx, s.bucket, objectKey, minio.RemoveObjectOptions{})
	if err != nil {
		return fmt.Errorf("failed to delete object: %w", err)
	}
	return nil
}

// DeleteThumb removes the thumbnail derived from a parent object key.
func (s *Store) DeleteThumb(ctx context.Context, parentObjectKey string) error {
	return s.Delete(ctx, parentObjectKey+".thumb.jpg")
}

// ChunkKey returns the object key for a chunk of an in-progress upload.
func ChunkKey(uploadID string, index int) string {
	return fmt.Sprintf("chunks/%s/%08d", uploadID, index)
}

// UploadChunk stores a single chunk of a large-file upload. Chunks are kept
// as independent objects so interrupted uploads can resume.
func (s *Store) UploadChunk(ctx context.Context, uploadID string, index int, reader io.Reader, size int64) error {
	key := ChunkKey(uploadID, index)
	_, err := s.client.PutObject(ctx, s.bucket, key, reader, size, minio.PutObjectOptions{})
	if err != nil {
		return fmt.Errorf("failed to upload chunk: %w", err)
	}
	return nil
}

// ListChunks returns the set of chunk indexes already uploaded for an upload.
// The result is a list of contiguous indexes 1..N, where N is the number of
// contiguous chunks present starting from chunk 1.
func (s *Store) ListChunks(ctx context.Context, uploadID string) (map[int]int64, error) {
	prefix := fmt.Sprintf("chunks/%s/", uploadID)
	existing := make(map[int]int64)
	for obj := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{
		Prefix: prefix,
	}) {
		if obj.Err != nil {
			return nil, obj.Err
		}
		var index int
		if _, err := fmt.Sscanf(obj.Key, prefix+"%08d", &index); err == nil {
			existing[index] = obj.Size
		}
	}
	return existing, nil
}

// AssembleChunks concatenates uploaded chunks into a single object. It reads
// chunks back from storage, so it must only be called with the full set of
// chunk indexes (1..total).
func (s *Store) AssembleChunks(ctx context.Context, uploadID string, totalChunks int, contentType, originalName string) (string, string, error) {
	ext := filepath.Ext(originalName)
	objectKey := fmt.Sprintf("%s/%s%s", time.Now().Format("2006/01/02"), uuid.NewString(), ext)

	var readers []io.ReadCloser
	total := int64(0)
	for i := 1; i <= totalChunks; i++ {
		obj, err := s.client.GetObject(ctx, s.bucket, ChunkKey(uploadID, i), minio.GetObjectOptions{})
		if err != nil {
			closeAll(readers)
			return "", "", fmt.Errorf("failed to open chunk %d: %w", i, err)
		}
		if _, err := obj.Stat(); err != nil {
			obj.Close()
			closeAll(readers)
			return "", "", fmt.Errorf("failed to stat chunk %d: %w", i, err)
		}
		readers = append(readers, obj)
		total += 0 // size is irrelevant for a streaming reader
	}

	multi := io.MultiReader(toReaders(readers)...)
	_, err := s.client.PutObject(ctx, s.bucket, objectKey, multi, -1, minio.PutObjectOptions{
		ContentType: contentType,
	})
	closeAll(readers)
	if err != nil {
		return "", "", fmt.Errorf("failed to assemble chunks: %w", err)
	}

	return objectKey, s.publicURL(objectKey), nil
}

// DeleteChunks removes all chunk objects for an upload.
func (s *Store) DeleteChunks(ctx context.Context, uploadID string) error {
	prefix := fmt.Sprintf("chunks/%s/", uploadID)
	for obj := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{
		Prefix: prefix,
	}) {
		if obj.Err != nil {
			return obj.Err
		}
		if err := s.client.RemoveObject(ctx, s.bucket, obj.Key, minio.RemoveObjectOptions{}); err != nil {
			return err
		}
	}
	return nil
}

func toReaders(readers []io.ReadCloser) []io.Reader {
	out := make([]io.Reader, 0, len(readers))
	for _, r := range readers {
		out = append(out, r)
	}
	return out
}

func closeAll(readers []io.ReadCloser) {
	for _, r := range readers {
		r.Close()
	}
}
