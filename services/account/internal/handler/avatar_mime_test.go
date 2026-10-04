package handler

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"testing"
)

func TestAvatarMimeAllowed(t *testing.T) {
	ok := []string{
		"image/png", "image/jpeg", "image/jpg", "image/gif", "image/webp",
		"image/png; charset=binary", "IMAGE/JPEG",
	}
	for _, ct := range ok {
		if !avatarMimeAllowed(ct) {
			t.Errorf("expected %q to be allowed", ct)
		}
	}

	bad := []string{
		"text/html", "image/svg+xml", "application/javascript",
		"application/xhtml+xml", "text/plain", "",
		"application/octet-stream", "application/x-httpd-php",
	}
	for _, ct := range bad {
		if avatarMimeAllowed(ct) {
			t.Errorf("expected %q to be rejected", ct)
		}
	}
}

// The claimed type must also agree with the actual bytes, otherwise an HTML
// document could be stored under an image Content-Type.
func TestAvatarDetectionMatchesClaim(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(0, 0, color.RGBA{1, 2, 3, 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode: %v", err)
	}
	det := http.DetectContentType(buf.Bytes())
	if !avatarMimeAllowed(det) {
		t.Fatalf("real PNG detected as %q but the guard rejected it", det)
	}

	html := []byte("<!DOCTYPE html><html><body><script>alert(1)</script></body></html>")
	if got := http.DetectContentType(html); avatarMimeAllowed(got) {
		t.Fatalf("HTML payload detected as %q passed the sniff check", got)
	}
}
