package storage

import "strings"

// Storage MIME policy.
//
// Two tiers:
//
//   - inlineSafe: whitelisted static media/document types that may be served
//     inline from buckets without becoming a stored-XSS vector. Browsers never
//     sniff script execution out of these.
//   - forbidden: active document / script formats that must never be stored,
//     because serving them (inline or downloaded same-origin) can execute code
//     in a browser.
//
// Everything else (archives, office documents, design files, encrypted
// containers such as OpenField's .ofe ciphertext, ...) is accepted and stored
// under its declared type; the internal file proxy additionally forces
// Content-Disposition: attachment for types that are not inline-safe so they
// always download instead of rendering.

// inlineSafeMimeTypes may be served directly to browsers.
var inlineSafeMimeTypes = map[string]bool{
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
	"application/pdf":   true,
	"application/json":  true,
	"text/plain":        true,
	"text/markdown":     true,
	"application/zip":   true,
	"application/gzip":  true,
	"application/x-tar": true,
	"font/woff":         true,
	"font/woff2":        true,
	"font/ttf":          true,
}

// forbiddenMimeTypes are rejected outright: browsers execute or actively
// render them, so storing them would create a stored-XSS / phishing vector.
var forbiddenMimeTypes = map[string]bool{
	"text/html":                true,
	"application/xhtml+xml":    true,
	"image/svg+xml":            true,
	"text/xml":                 true,
	"application/xml":          true,
	"text/javascript":          true,
	"application/javascript":   true,
	"application/x-javascript": true,
	"application/ecmascript":   true,
	"application/x-ecmascript": true,
	"application/wasm":         true,
	"application/x-httpd-php":  true,
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

// MimeAllowed reports whether the given content type may be stored at all.
// Active document/script formats are rejected; every inert binary or document
// format passes, including application/octet-stream (unknown binaries and
// encrypted containers).
func MimeAllowed(contentType string) bool {
	return !forbiddenMimeTypes[NormalizeMimeType(contentType)]
}

// InlineSafeMime reports whether the type may be rendered inline by browsers.
// Types outside this set are served as downloads (Content-Disposition:
// attachment) through the internal file proxy.
func InlineSafeMime(contentType string) bool {
	return inlineSafeMimeTypes[NormalizeMimeType(contentType)]
}
