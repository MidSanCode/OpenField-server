package storage

import (
	"context"
	"fmt"

	"github.com/openfield/server/pkg/config"
	"github.com/openfield/server/pkg/model"
)

// Manager owns one Store per configured logical bucket and resolves which store
// a request should use based on a user's selected storage_bucket.
type Manager struct {
	cfg    config.StorageConfig
	stores map[string]*Store
	order  []config.StorageBucketConfig
}

// Enabled reports whether the manager has at least one usable store.
func (m *Manager) Enabled() bool {
	if m == nil {
		return false
	}
	for _, s := range m.stores {
		if s.Enabled() {
			return true
		}
	}
	return false
}

// Default returns the store for the default bucket. It returns nil when storage
// is not configured.
func (m *Manager) Default() *Store {
	if m == nil {
		return nil
	}
	return m.stores[m.cfg.DefaultBucket().Name]
}

// For returns the store backing the given logical bucket name, falling back to
// the default bucket for unknown or empty names. It returns nil when storage is
// not configured.
func (m *Manager) For(name string) *Store {
	if m == nil {
		return nil
	}
	if s, ok := m.stores[name]; ok {
		return s
	}
	return m.Default()
}

// ForPhysical returns the store writing to the given physical S3 bucket name,
// used by the internal file proxy to resolve URLs like /files/<bucket>/<key>.
// It returns nil when no configured store uses that physical bucket.
func (m *Manager) ForPhysical(physicalBucket string) *Store {
	if m == nil {
		return nil
	}
	for _, s := range m.stores {
		if s != nil && s.bucket == physicalBucket {
			return s
		}
	}
	return nil
}

// SignAttachment refreshes an attachment's URL/ThumbURL/PreviewURL to
// time-limited presigned GET URLs. It is a no-op when the manager has no
// usable store or presigning is disabled (the stored URLs are kept as-is).
// Thumbnail/preview URLs are only refreshed when the attachment actually has
// them (the empty-string check doubles as the "no rendition" signal).
func (m *Manager) SignAttachment(ctx context.Context, att *model.Attachment) error {
	if m == nil || att == nil {
		return nil
	}
	store := m.For(att.Bucket)
	if store == nil || !store.Enabled() || !store.presignEnabled {
		return nil
	}
	signed, err := store.SignObjectURL(ctx, att.ObjectKey)
	if err != nil {
		return fmt.Errorf("failed to sign attachment URL: %w", err)
	}
	att.URL = signed
	if att.ThumbURL != "" {
		thumb, err := store.SignObjectURL(ctx, att.ObjectKey+".thumb.jpg")
		if err != nil {
			return fmt.Errorf("failed to sign thumbnail URL: %w", err)
		}
		att.ThumbURL = thumb
	}
	if att.PreviewURL != "" {
		preview, err := store.SignObjectURL(ctx, att.ObjectKey+".preview.jpg")
		if err != nil {
			return fmt.Errorf("failed to sign preview URL: %w", err)
		}
		att.PreviewURL = preview
	}
	return nil
}

// Buckets returns the configured logical buckets in config order.
func (m *Manager) Buckets() []config.StorageBucketConfig {
	if m == nil {
		return nil
	}
	return m.order
}

// BucketByName returns the logical bucket config for a name, plus whether an
// exact match was found. Unknown names resolve to the default bucket config.
func (m *Manager) BucketByName(name string) (config.StorageBucketConfig, bool) {
	if m == nil {
		return config.StorageBucketConfig{}, false
	}
	return m.cfg.BucketByName(name)
}

// DefaultBucket returns the default logical bucket config.
func (m *Manager) DefaultBucket() config.StorageBucketConfig {
	if m == nil {
		return config.StorageBucketConfig{}
	}
	return m.cfg.DefaultBucket()
}
