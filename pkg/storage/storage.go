// Package storage wraps the S3-compatible object store (MinIO, AWS S3,
// RustFS, ...): bucket management, uploads, MIME handling and presigned or
// gateway-proxied object URLs.
package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// Store wraps an S3-compatible object store client (MinIO, AWS S3, RustFS, ...)
// for a single physical bucket.
type Store struct {
	client        *minio.Client
	bucket        string
	publicBaseURL string
	enabled       bool
	// name is the logical bucket id from the storage config. The empty string
	// means "unconfigured / default".
	name string
	// proxied marks internal-proxy mode: generated URLs point at the gateway
	// (<public_base_url>/<bucket>/<key>) and objects are streamed back by the
	// storage service, so buckets never need public read access.
	proxied bool
	// presignEnabled switches URL generation to time-limited presigned GET
	// URLs so the physical bucket can stay private while clients download
	// objects directly from the S3 endpoint.
	presignEnabled bool
	// presignTTL is how long a presigned read URL stays valid.
	presignTTL time.Duration
}

// IsConfigured reports whether object storage has been configured. When no
// endpoint or bucket is set, services start without object storage and all
// upload endpoints return an error.
func IsConfigured(cfg config.StorageConfig) bool {
	if cfg.Endpoint == "" && cfg.InternalEndpoint == "" {
		return false
	}
	for _, b := range cfg.BucketList() {
		if b.Bucket != "" {
			return true
		}
	}
	return false
}

// Enabled reports whether this store has a backing object store. When false,
// all upload/delete operations must not be called.
func (s *Store) Enabled() bool {
	return s != nil && s.enabled
}

// Name returns the logical bucket id of this store.
func (s *Store) Name() string {
	if s == nil {
		return ""
	}
	return s.name
}

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

// New creates a storage manager from config, one Store per logical bucket.
// When object storage is not configured it returns a manager without stores
// (never an error), so services can still start without storage. The bucket
// existence check is best-effort: an unreachable or not-yet-created bucket
// never fails startup — operations fail at request time (mapped to an error
// response) and recover automatically once the storage backend is available
// again.
func New(cfg config.StorageConfig) (*Manager, error) {
	m := &Manager{
		cfg:    cfg,
		stores: make(map[string]*Store),
		order:  cfg.BucketList(),
	}
	if !IsConfigured(cfg) {
		logger.Log.Warn("storage not configured; running without object storage")
		return m, nil
	}

	// Server-side S3 calls go through the internal endpoint so the storage
	// service reaches S3 over a fast in-VPC address while the public
	// Endpoint (used to derive default URLs) can keep pointing at a CDN or
	// public hostname. InternalEndpoint falls back to Endpoint when unset.
	internalEndpoint := normalizeEndpoint(cfg.ResolveInternalEndpoint())
	publicEndpoint := cfg.Endpoint
	client, err := minio.New(internalEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		// A malformed endpoint must not take the service down either.
		logger.Log.Error("failed to create storage client; running without object storage", "error", err)
		return m, nil
	}

	// Internal-proxy mode: URLs go through the gateway's file route. The
	// public base must be configured explicitly, since only the operator knows
	// the client-facing gateway address; fall back to direct URLs otherwise.
	proxied := cfg.InternalProxy.Enabled
	if proxied && cfg.PublicBaseURL == "" {
		logger.Log.Warn("internal_proxy enabled but storage.public_base_url is empty; falling back to direct bucket URLs")
		proxied = false
	}
	if cfg.Presign.Enabled && proxied {
		logger.Log.Warn("storage presign and internal_proxy are both enabled; presign wins for attachment URLs")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	for _, b := range m.order {
		store := &Store{
			client:         client,
			bucket:         b.Bucket,
			publicBaseURL:  resolvePublicBaseURL(cfg, b),
			enabled:        true,
			name:           b.Name,
			proxied:        proxied,
			presignEnabled: cfg.Presign.Enabled,
			presignTTL:     time.Duration(cfg.Presign.PresignGetTTL()) * time.Second,
		}
		if proxied {
			store.publicBaseURL = strings.TrimRight(cfg.PublicBaseURL, "/")
		}

		exists, err := client.BucketExists(ctx, store.bucket)
		if err != nil {
			logger.Log.Warn("storage bucket unavailable at startup", "bucket", store.bucket, "error", err)
		} else if !exists {
			if err := client.MakeBucket(ctx, store.bucket, minio.MakeBucketOptions{Region: cfg.Region}); err != nil {
				logger.Log.Warn("failed to create storage bucket", "bucket", store.bucket, "error", err)
			} else {
				logger.Log.Info("storage bucket created", "bucket", store.bucket)
			}
		}

		m.stores[b.Name] = store
		if cfg.InternalEndpoint != "" && cfg.InternalEndpoint != cfg.Endpoint {
			logger.Log.Info("storage initialized",
				"internal_endpoint", internalEndpoint,
				"public_endpoint", publicEndpoint,
				"bucket", store.bucket,
				"logical", b.Name)
		} else {
			logger.Log.Info("storage initialized",
				"endpoint", publicEndpoint,
				"bucket", store.bucket,
				"logical", b.Name)
		}
	}
	return m, nil
}

// resolvePublicBaseURL derives the base URL used to build public object URLs
// for a logical bucket. A per-bucket PublicBaseURL wins, then the shared
// config value; otherwise it is derived from the endpoint so AWS S3 and
// S3-compatible stores work out of the box.
func resolvePublicBaseURL(cfg config.StorageConfig, b config.StorageBucketConfig) string {
	if b.PublicBaseURL != "" {
		return strings.TrimRight(b.PublicBaseURL, "/")
	}
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
		return fmt.Sprintf("https://%s.s3.%s.amazonaws.com", b.Bucket, region)
	}
	return fmt.Sprintf("%s://%s/%s", scheme, normalizeEndpoint(cfg.Endpoint), b.Bucket)
}

// publicURL builds the full URL for an object key. In internal-proxy mode the
// URL points at the gateway's file route with the physical bucket and key in
// the path (<public_base_url>/<bucket>/<key>); otherwise it targets the
// bucket host directly (the base may already contain a bucket path, e.g.
// http://host/openfield).
func (s *Store) publicURL(objectKey string) string {
	if s.proxied {
		return fmt.Sprintf("%s/%s/%s", s.publicBaseURL, s.bucket, objectKey)
	}
	base, err := url.Parse(s.publicBaseURL)
	if err != nil {
		return s.publicBaseURL + "/" + objectKey
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/" + objectKey
	return base.String()
}

// rewriteHost points a presigned URL at the client-reachable host. minio signs
// against the internal endpoint (e.g. 127.0.0.1:9000), which clients cannot
// reach; the public base's scheme and host replace it while the rest of the
// URL (bucket path + X-Amz query) is preserved.
func rewriteHost(u *url.URL, publicBase string) (string, error) {
	base, err := url.Parse(publicBase)
	if err != nil || base.Host == "" {
		return u.String(), nil
	}
	u.Scheme = base.Scheme
	u.Host = base.Host
	return u.String(), nil
}

// SignObjectURL returns the read URL for an object key. In presign mode it is
// a time-limited signed URL clients can fetch directly from the S3 endpoint;
// otherwise the legacy public/proxy URL is returned.
func (s *Store) SignObjectURL(ctx context.Context, objectKey string) (string, error) {
	if !s.presignEnabled {
		return s.publicURL(objectKey), nil
	}
	u, err := s.client.PresignedGetObject(ctx, s.bucket, objectKey, s.presignTTL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to sign object URL: %w", err)
	}
	return rewriteHost(u, s.publicBaseURL)
}

// SignPutURL returns a time-limited presigned PUT URL for direct client
// uploads (reserved for future use; uploads currently proxy through the
// storage service with server-side credentials).
func (s *Store) SignPutURL(ctx context.Context, objectKey string) (string, error) {
	u, err := s.client.PresignedPutObject(ctx, s.bucket, objectKey, s.presignTTL)
	if err != nil {
		return "", fmt.Errorf("failed to sign upload URL: %w", err)
	}
	return rewriteHost(u, s.publicBaseURL)
}

// Open streams an object for server-side delivery (internal proxy mode). The
// caller owns the returned object and must Close it; metadata such as size
// and content type is read lazily via [minio.Object.Stat].
func (s *Store) Open(ctx context.Context, objectKey string) (*minio.Object, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, objectKey, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to open object: %w", err)
	}
	return obj, nil
}

// OpenOpts is [Open] with explicit options (used for Range requests).
func (s *Store) OpenOpts(ctx context.Context, objectKey string, opts minio.GetObjectOptions) (*minio.Object, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, objectKey, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to open object: %w", err)
	}
	return obj, nil
}

// Bucket returns the physical S3 bucket name this store writes to.
func (s *Store) Bucket() string {
	if s == nil {
		return ""
	}
	return s.bucket
}

// userPrefix builds the per-user object-key prefix so every uploaded object
// lives under the owner's directory ("users/<id>/..."), isolating attachments
// by user at the storage level.
func userPrefix(userID int64) string {
	return fmt.Sprintf("users/%d", userID)
}

// Upload stores a file under the owner's user directory and returns its
// object key and (signed or public) URL.
func (s *Store) Upload(ctx context.Context, userID int64, reader io.Reader, size int64, contentType, originalName string) (string, string, error) {
	ext := filepath.Ext(originalName)
	objectKey := fmt.Sprintf("%s/%s/%s%s", userPrefix(userID), time.Now().Format("2006/01/02"), uuid.NewString(), ext)

	_, err := s.client.PutObject(ctx, s.bucket, objectKey, reader, size, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return "", "", fmt.Errorf("failed to upload object: %w", err)
	}

	url, err := s.SignObjectURL(ctx, objectKey)
	if err != nil {
		return "", "", err
	}
	return objectKey, url, nil
}

// UploadThumb stores a thumbnail image next to an object and returns its URL.
// The thumbnail key is derived from the parent object key with a ".thumb.jpg" suffix.
func (s *Store) UploadThumb(ctx context.Context, parentObjectKey string, reader io.Reader, size int64) (string, error) {
	thumbKey := parentObjectKey + ".thumb.jpg"
	_, err := s.client.PutObject(ctx, s.bucket, thumbKey, reader, size, minio.PutObjectOptions{
		ContentType: "image/jpeg",
	})
	if err != nil {
		return "", fmt.Errorf("failed to upload thumbnail: %w", err)
	}
	return s.SignObjectURL(ctx, thumbKey)
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

// UploadPreview stores the mid-size preview rendition derived from a parent
// object key and returns its URL.
func (s *Store) UploadPreview(ctx context.Context, parentObjectKey string, reader io.Reader, size int64) (string, error) {
	previewKey := parentObjectKey + ".preview.jpg"
	_, err := s.client.PutObject(ctx, s.bucket, previewKey, reader, size, minio.PutObjectOptions{
		ContentType: "image/jpeg",
	})
	if err != nil {
		return "", fmt.Errorf("failed to upload preview: %w", err)
	}
	return s.SignObjectURL(ctx, previewKey)
}

// DeletePreview removes the preview derived from a parent object key.
func (s *Store) DeletePreview(ctx context.Context, parentObjectKey string) error {
	return s.Delete(ctx, parentObjectKey+".preview.jpg")
}

// ChunkKey returns the object key for a chunk of an in-progress upload. Chunks
// live under the uploading user's directory so intermediate state is isolated
// per user like every other object.
func ChunkKey(userID int64, uploadID string, index int) string {
	return fmt.Sprintf("%s/chunks/%s/%08d", userPrefix(userID), uploadID, index)
}

// UploadChunk stores a single chunk of a large-file upload. Chunks are kept
// as independent objects so interrupted uploads can resume.
func (s *Store) UploadChunk(ctx context.Context, userID int64, uploadID string, index int, reader io.Reader, size int64) error {
	key := ChunkKey(userID, uploadID, index)
	_, err := s.client.PutObject(ctx, s.bucket, key, reader, size, minio.PutObjectOptions{})
	if err != nil {
		return fmt.Errorf("failed to upload chunk: %w", err)
	}
	return nil
}

// chunkPrefix returns the prefix under which a user's chunked upload lives.
func chunkPrefix(userID int64, uploadID string) string {
	return fmt.Sprintf("%s/chunks/%s/", userPrefix(userID), uploadID)
}

// ListChunks returns the set of chunk indexes already uploaded for an upload
// by listing the chunk prefix. NOTE: listing-based discovery is unreliable
// across S3-compatible backends (RustFS has been observed to omit objects
// under a multi-segment prefix right after they were PutObject-ed), so
// callers that need a definitive "which chunks exist?" answer must use
// StatChunks instead. Kept only as a best-effort debugging aid.
func (s *Store) ListChunks(ctx context.Context, userID int64, uploadID string) (map[int]int64, error) {
	prefix := chunkPrefix(userID, uploadID)
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

// StatChunks returns the set of chunk indexes already present for an upload
// by stat-ing each expected chunk key (chunks/<uploadID>/<index>) directly.
//
// This intentionally avoids ListObjects: S3-compatible backends disagree on
// prefix+delimiter listing semantics, and RustFS (the current backend) omits
// freshly written objects from prefix listings, which made ChunkComplete
// report every chunk as missing while the client had received a 200 for each
// PUT. The key layout is fully owned by ChunkKey, so stat-ing each known key
// is deterministic — there is nothing to enumerate or parse.
func (s *Store) StatChunks(ctx context.Context, userID int64, uploadID string, totalChunks int) (map[int]int64, error) {
	existing := make(map[int]int64, totalChunks)
	for i := 1; i <= totalChunks; i++ {
		info, err := s.client.StatObject(ctx, s.bucket, ChunkKey(userID, uploadID, i), minio.StatObjectOptions{})
		if err != nil {
			resp := minio.ToErrorResponse(err)
			if resp.Code == "NoSuchKey" || resp.Code == "NotFound" || resp.StatusCode == 404 {
				continue
			}
			return nil, fmt.Errorf("failed to stat chunk %d: %w", i, err)
		}
		existing[i] = info.Size
	}
	return existing, nil
}

// AssembleChunks concatenates uploaded chunks into a single object. It reads
// chunks back from storage, so it must only be called with the full set of
// chunk indexes (1..total). The SHA-256 of the assembled bytes is computed
// while streaming so callers can deduplicate uploads.
func (s *Store) AssembleChunks(ctx context.Context, userID int64, uploadID string, totalChunks int, contentType, originalName string) (objectKey, signedURL, sha256Hex string, err error) {
	ext := filepath.Ext(originalName)
	objectKey = fmt.Sprintf("%s/%s/%s%s", userPrefix(userID), time.Now().Format("2006/01/02"), uuid.NewString(), ext)

	var readers []io.ReadCloser
	for i := 1; i <= totalChunks; i++ {
		obj, err := s.client.GetObject(ctx, s.bucket, ChunkKey(userID, uploadID, i), minio.GetObjectOptions{})
		if err != nil {
			closeAll(readers)
			return "", "", "", fmt.Errorf("failed to open chunk %d: %w", i, err)
		}
		if _, err := obj.Stat(); err != nil {
			obj.Close()
			closeAll(readers)
			return "", "", "", fmt.Errorf("failed to stat chunk %d: %w", i, err)
		}
		readers = append(readers, obj)
	}

	hasher := sha256.New()
	tee := io.TeeReader(io.MultiReader(toReaders(readers)...), hasher)
	_, err = s.client.PutObject(ctx, s.bucket, objectKey, tee, -1, minio.PutObjectOptions{
		ContentType: contentType,
	})
	closeAll(readers)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to assemble chunks: %w", err)
	}

	signed, serr := s.SignObjectURL(ctx, objectKey)
	if serr != nil {
		return "", "", "", serr
	}
	return objectKey, signed, hex.EncodeToString(hasher.Sum(nil)), nil
}

// DeleteChunks removes all chunk objects for an upload.
func (s *Store) DeleteChunks(ctx context.Context, userID int64, uploadID string) error {
	prefix := chunkPrefix(userID, uploadID)
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
