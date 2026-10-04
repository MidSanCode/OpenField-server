package handler

import (
	"bytes"
	"fmt"
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

// maxImagePixels bounds the decoded pixel count of any image we are willing to
// decode. The byte limits above bound only the COMPRESSED size, while decode
// memory is decided by the dimensions declared in the header: a few-hundred-KB
// PNG whose IHDR claims 60000x60000 makes png.Decode allocate ~14GB. The Go
// decoders check for overflow but impose no absolute pixel ceiling, and the mime
// type that selects the decoder comes from the client. 50 MP comfortably covers
// a 8000x6000 camera photo while refusing a bomb.
const maxImagePixels = 50_000_000

// maxImageDim additionally rejects an extreme aspect ratio that would keep the
// pixel product under the cap while still allocating a huge single row.
const maxImageDim = 20_000

// checkImageDimensions reads only the header and rejects an image whose
// declared size would make decoding allocate an unreasonable amount of memory.
// It must be called BEFORE the full decode, because that is what allocates.
func checkImageDimensions(data []byte, mimeType string) error {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		// An unreadable header is not necessarily fatal here: fall through and
		// let the real decode produce the authoritative error. The pixel guard
		// only needs to succeed when the header IS readable.
		return nil
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return fmt.Errorf("image has invalid dimensions %dx%d", cfg.Width, cfg.Height)
	}
	if cfg.Width > maxImageDim || cfg.Height > maxImageDim {
		return fmt.Errorf("image dimensions %dx%d exceed the %d px limit", cfg.Width, cfg.Height, maxImageDim)
	}
	if cfg.Width*cfg.Height > maxImagePixels {
		return fmt.Errorf("image has too many pixels (%dx%d)", cfg.Width, cfg.Height)
	}
	return nil
}

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
	// Reject an oversized image BEFORE decoding, which is where the memory is
	// allocated.
	if err := checkImageDimensions(data, mimeType); err != nil {
		return nil, err
	}
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

// maxPreviewDim is the longest edge of a generated image preview. Larger than
// the thumbnail so quick viewing stays sharp on phones, far smaller than a
// typical photo so the grid -> viewer flow does not pull the original.
const maxPreviewDim = 1440

// generatePreview downscales an image to at most maxPreviewDim on its longest
// edge and encodes it as JPEG at a slightly higher quality than the
// thumbnail. Images already within the bound are re-encoded anyway: the goal
// is a uniformly light rendition, and JPEG quality 82 alone typically halves
// a camera original. Returns nil if the input cannot be decoded.
func generatePreview(data []byte, mimeType string) ([]byte, error) {
	if err := checkImageDimensions(data, mimeType); err != nil {
		return nil, err
	}
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
	if w <= maxPreviewDim && h <= maxPreviewDim {
		tw, th = w, h
	} else if w >= h {
		tw = maxPreviewDim
		th = int(float64(h) * float64(maxPreviewDim) / float64(w))
	} else {
		th = maxPreviewDim
		tw = int(float64(w) * float64(maxPreviewDim) / float64(h))
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
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 82}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
