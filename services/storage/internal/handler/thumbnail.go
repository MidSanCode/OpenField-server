package handler

import (
	"bytes"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"strings"

	"golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

// maxThumbDim is the longest edge of a generated image thumbnail.
const maxThumbDim = 512

// maxStripReadBytes caps the size of an image that gets GPS/location metadata
// stripped on upload. Larger uploads keep their bytes untouched (the thumbnail
// still discards all metadata).
const maxStripReadBytes = 64 * 1024 * 1024

// decodeImage decodes an image based on its mime type, with PNG fallback.
func decodeImage(r io.Reader, mimeType string) (image.Image, error) {
	switch {
	case strings.Contains(mimeType, "png"):
		return png.Decode(r)
	case strings.Contains(mimeType, "gif"):
		return gif.Decode(r)
	case strings.Contains(mimeType, "webp"):
		return webp.Decode(r)
	default:
		return jpeg.Decode(r)
	}
}

// isImageMime reports whether the mime type is an image we can decode.
func isImageMime(mimeType string) bool {
	for _, p := range []string{"image/", "application/"} {
		if strings.HasPrefix(mimeType, p) {
			if strings.Contains(mimeType, "jpg") || strings.Contains(mimeType, "jpeg") ||
				strings.Contains(mimeType, "png") || strings.Contains(mimeType, "gif") ||
				strings.Contains(mimeType, "webp") {
				return true
			}
		}
	}
	return false
}

// generateThumbnail downscales an image to at most maxThumbDim on its longest
// edge and encodes it as JPEG. Returns nil if the input cannot be decoded.
func generateThumbnail(data []byte, mimeType string) ([]byte, error) {
	img, err := decodeImage(bytes.NewReader(data), mimeType)
	if err != nil {
		return nil, err
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= 0 || h <= 0 {
		return nil, image.ErrFormat
	}

	var tw, th int
	if w <= maxThumbDim && h <= maxThumbDim {
		tw, th = w, h
	} else if w >= h {
		tw = maxThumbDim
		th = int(float64(h) * float64(maxThumbDim) / float64(w))
	} else {
		th = maxThumbDim
		tw = int(float64(w) * float64(maxThumbDim) / float64(h))
	}
	if th < 1 {
		th = 1
	}
	if tw < 1 {
		tw = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, bounds, draw.Over, nil)

	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 80}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// makeThumbnailReader is a convenience wrapper for handlers.
func makeThumbnailReader(data []byte, mimeType string) (*bytes.Reader, error) {
	thumb, err := generateThumbnail(data, mimeType)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(thumb), nil
}
