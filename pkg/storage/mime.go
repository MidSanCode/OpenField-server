package storage

import "strings"

// allowedMimeTypes is the whitelist of content types users may upload.
//
// It doubles as a stored-XSS defense: active document types (HTML, XHTML, SVG,
// JavaScript, ...) are deliberately excluded, so an object fetched straight
// from the public bucket can never execute script in the serving origin.
// Browsers sniff content only when the type is unknown; whitelisted static
// media types keep them out of that path.
var allowedMimeTypes = map[string]bool{
	// Images
	"image/jpeg": true,
	"image/png":  true,
	"image/gif":  true,
	"image/webp": true,
	"image/bmp":  true,
	"image/avif": true,
	"image/tiff": true,

	// Video
	"video/mp4":       true,
	"video/webm":      true,
	"video/quicktime": true,

	// Audio
	"audio/mpeg":  true,
	"audio/ogg":   true,
	"audio/wav":   true,
	"audio/x-wav": true,
	"audio/mp4":   true,
	"audio/flac":  true,

	// Documents / data
	"application/pdf":     true,
	"application/json":    true,
	"text/plain":          true,
	"text/markdown":       true,
	"application/zip":     true,
	"application/gzip":    true,
	"application/x-tar":   true,
	"font/woff":           true,
	"font/woff2":          true,
	"font/ttf":            true,
	"application/octet-stream": false, // explicit: binary blobs without a known type stay rejected
}

// NormalizeMimeType strips parameters ("; charset=...") and lowercases the
// media type.
func NormalizeMimeType(contentType string) string {
	ct := strings.TrimSpace(strings.ToLower(contentType))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	return ct
}

// MimeAllowed reports whether the given content type may be stored. Unknown
// and parameterized-but-unrecognized types are rejected.
func MimeAllowed(contentType string) bool {
	return allowedMimeTypes[NormalizeMimeType(contentType)]
}
