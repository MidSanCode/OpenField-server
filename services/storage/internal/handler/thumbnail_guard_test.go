package handler

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// makePNGWithDeclaredSize builds a PNG whose IHDR declares w x h but whose
// pixel data is trivial, i.e. a decompression bomb: the compressed bytes stay
// small while the decoder would allocate by the declared dimensions.
func makePNGWithDeclaredSize(t *testing.T, w, h uint32) []byte {
	t.Helper()

	var buf bytes.Buffer
	buf.Write([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A})

	// IHDR: width, height, bit depth 8, colour type 2 (RGB), then zeros.
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], w)
	binary.BigEndian.PutUint32(ihdr[4:8], h)
	ihdr[8] = 8  // bit depth
	ihdr[9] = 2  // colour type: truecolour
	writeChunk(&buf, "IHDR", ihdr)

	// A single empty IDAT is enough for png.DecodeConfig to report the header
	// dimensions; the full decode will fail later, which is fine — the guard
	// must reject before we ever get there.
	writeChunk(&buf, "IDAT", []byte{0x78, 0x9c, 0x03, 0x00, 0x00, 0x00, 0x00, 0x01})
	writeChunk(&buf, "IEND", nil)
	return buf.Bytes()
}

func writeChunk(buf *bytes.Buffer, typ string, data []byte) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(data)))
	buf.Write(length[:])
	payload := append([]byte(typ), data...)
	buf.Write(payload)
	var crc [4]byte
	binary.BigEndian.PutUint32(crc[:], crc32.ChecksumIEEE(payload))
	buf.Write(crc[:])
}

// A small PNG declaring an enormous size must be refused without decoding.
func TestCheckImageDimensionsRejectsBomb(t *testing.T) {
	bomb := makePNGWithDeclaredSize(t, 60000, 60000)
	t.Logf("bomb is %d bytes compressed, declares %d pixels", len(bomb), 60000*60000)

	if err := checkImageDimensions(bomb, "image/png"); err == nil {
		t.Fatal("guard accepted a 3.6-gigapixel image")
	} else {
		t.Logf("guard rejected: %v", err)
	}

	// And the thumbnail path must refuse it too, rather than allocating.
	if _, err := generateThumbnail(bomb, "image/png"); err == nil {
		t.Fatal("generateThumbnail accepted the bomb")
	}
	if _, err := generatePreview(bomb, "image/png"); err == nil {
		t.Fatal("generatePreview accepted the bomb")
	}
}

// A legitimate, modestly sized image must still pass and produce a thumbnail.
func TestCheckImageDimensionsAllowsNormalImage(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 800, 600))
	for x := 0; x < 800; x++ {
		for y := 0; y < 600; y++ {
			img.Set(x, y, color.RGBA{uint8(x % 256), uint8(y % 256), 128, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode: %v", err)
	}
	data := buf.Bytes()

	if err := checkImageDimensions(data, "image/png"); err != nil {
		t.Fatalf("guard rejected a legitimate 800x600 image: %v", err)
	}
	thumb, err := generateThumbnail(data, "image/png")
	if err != nil {
		t.Fatalf("generateThumbnail: %v", err)
	}
	if len(thumb) == 0 {
		t.Fatal("empty thumbnail")
	}
	t.Logf("800x600 png -> %d byte thumbnail", len(thumb))
}
