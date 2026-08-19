package imaging

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"testing"
)

func u16le(b []byte, off int, v uint16) { binary.LittleEndian.PutUint16(b[off:off+2], v) }
func u32le(b []byte, off int, v uint32) { binary.LittleEndian.PutUint32(b[off:off+4], v) }

// buildTiffLE returns a little-endian TIFF buffer with an IFD0 that carries
// Make, Orientation, an Exif IFD pointer and a GPS IFD pointer, plus a GPS IFD
// with latitude/longitude RATIONAL values stored at offsets.
func buildTiffLE() []byte {
	b := make([]byte, 264)
	b[0], b[1] = 'I', 'I'
	u16le(b, 2, 42)
	u32le(b, 4, 8) // IFD0 at 8

	// IFD0
	u16le(b, 8, 5)
	// entry: Make (0x010F, ASCII, count 11, value -> offset 76)
	u16le(b, 10, 0x010F)
	u16le(b, 12, 2)
	u32le(b, 14, 11)
	u32le(b, 18, 76)
	// entry: Orientation (0x0112, SHORT, count 1, inline value 6)
	u16le(b, 22, 0x0112)
	u16le(b, 24, 3)
	u32le(b, 26, 1)
	u32le(b, 30, 6)
	// entry: Exif IFD pointer (0x8769, LONG, -> 108)
	u16le(b, 34, 0x8769)
	u16le(b, 36, 4)
	u32le(b, 38, 1)
	u32le(b, 42, 108)
	// entry: GPS IFD pointer (0x8825, LONG, -> 140)
	u16le(b, 46, 0x8825)
	u16le(b, 48, 4)
	u32le(b, 50, 1)
	u32le(b, 54, 140)
	// entry: DateTime (0x0132, ASCII, count 20, value -> offset 88)
	u16le(b, 58, 0x0132)
	u16le(b, 60, 2)
	u32le(b, 62, 20)
	u32le(b, 66, 88)
	u32le(b, 70, 0) // next IFD

	copy(b[76:87], "TestCamera\x00")
	copy(b[88:108], "2024:01:01 00:00:00\x00")

	// Exif IFD at 108
	u16le(b, 108, 1)
	u16le(b, 110, 0xA002) // PixelXDimension
	u16le(b, 112, 4)
	u32le(b, 114, 1)
	u32le(b, 118, 4032)
	u32le(b, 122, 0)

	// GPS IFD at 140
	u16le(b, 140, 5)
	// GPSVersionID (0x0000, BYTE, count 4, inline)
	u16le(b, 142, 0x0000)
	u16le(b, 144, 1)
	u32le(b, 146, 4)
	u32le(b, 150, 0x00000302)
	// GPSLatitudeRef (0x0001, ASCII, count 2, inline "N")
	u16le(b, 154, 0x0001)
	u16le(b, 156, 2)
	u32le(b, 158, 2)
	u32le(b, 162, 'N') // inline value bytes: 'N',0,0,0
	// GPSLatitude (0x0002, RATIONAL x3, -> 208)
	u16le(b, 166, 0x0002)
	u16le(b, 168, 5)
	u32le(b, 170, 3)
	u32le(b, 174, 208)
	// GPSLongitudeRef (0x0003, ASCII, count 2, inline "E")
	u16le(b, 178, 0x0003)
	u16le(b, 180, 2)
	u32le(b, 182, 2)
	u32le(b, 186, 'E')
	// GPSLongitude (0x0004, RATIONAL x3, -> 232)
	u16le(b, 190, 0x0004)
	u16le(b, 192, 5)
	u32le(b, 194, 3)
	u32le(b, 198, 232)
	u32le(b, 202, 0) // next IFD

	// GPSLatitude rationals at 208..231
	rationals(b, 208, []uint32{52000000, 13000000, 31000000})
	// GPSLongitude rationals at 232..255
	rationals(b, 232, []uint32{83000000, 27000000, 19000000})
	return b
}

func rationals(b []byte, off int, values []uint32) {
	const den = uint32(1000000)
	for i, v := range values {
		u32le(b, off+i*8, v)
		u32le(b, off+i*8+4, den)
	}
}

func zeroed(b []byte, from, to int) bool {
	for i := from; i < to; i++ {
		if b[i] != 0 {
			return false
		}
	}
	return true
}

func TestStripExifGPSZeroesGPSButKeepsOthers(t *testing.T) {
	tiff := buildTiffLE()
	out := stripExifGPS(tiff)

	if bytes.Equal(out, tiff) {
		t.Fatal("expected GPS stripping to change the buffer")
	}
	if string(out[76:87]) != "TestCamera\x00" {
		t.Error("Make metadata was modified")
	}
	if string(out[88:108]) != "2024:01:01 00:00:00\x00" {
		t.Error("DateTime metadata was modified")
	}
	if binary.LittleEndian.Uint16(out[110:112]) != 0xA002 || binary.LittleEndian.Uint32(out[118:122]) != 4032 {
		t.Error("Exif IFD contents were modified")
	}
	if binary.LittleEndian.Uint32(out[30:34]) != 6 {
		t.Error("Orientation was modified")
	}
	if !zeroed(out, 208, 232) {
		t.Error("GPSLatitude was not zeroed")
	}
	if !zeroed(out, 232, 256) {
		t.Error("GPSLongitude was not zeroed")
	}
	if !zeroed(out, 150, 154) {
		t.Error("GPSVersionID was not zeroed")
	}
	if !zeroed(out, 162, 166) {
		t.Error("GPSLatitudeRef was not zeroed")
	}
}

func TestStripExifGPSNoGPSReturnsSameBytes(t *testing.T) {
	tiff := buildTiffLE()
	// Point the GPS IFD pointer away from a valid IFD so no GPS is found.
	u32le(tiff, 54, 5000)
	out := stripExifGPS(tiff)
	if !bytes.Equal(out, tiff) {
		t.Fatal("expected unchanged output when no GPS IFD exists")
	}
}

func TestStripJPEGLocation(t *testing.T) {
	tiff := buildTiffLE()
	var jpeg bytes.Buffer
	jpeg.Write([]byte{0xFF, 0xD8}) // SOI
	payload := append([]byte("Exif\x00\x00"), tiff...)
	jpeg.Write([]byte{0xFF, 0xE1})
	var l [2]byte
	binary.BigEndian.PutUint16(l[:], uint16(2+len(payload)))
	jpeg.Write(l[:])
	jpeg.Write(payload)
	jpeg.Write([]byte{0xFF, 0xD9}) // EOI

	out := StripImageLocation(jpeg.Bytes(), "image/jpeg")
	if bytes.Equal(out, jpeg.Bytes()) {
		t.Fatal("expected the JPEG to be rewritten")
	}
	if !bytes.Equal(out[0:2], []byte{0xFF, 0xD8}) || !bytes.Equal(out[len(out)-2:], []byte{0xFF, 0xD9}) {
		t.Fatal("JPEG frame markers were damaged")
	}
	// Extract the APP1 EXIF tiff and confirm GPS zeroing.
	if out[2] != 0xFF || out[3] != 0xE1 {
		t.Fatal("APP1 segment missing")
	}
	segLen := int(binary.BigEndian.Uint16(out[4:6]))
	inner := out[6 : 6+segLen]
	if string(inner[:6]) != "Exif\x00\x00" {
		t.Fatal("EXIF prefix damaged")
	}
	cleaned := inner[6:]
	if !zeroed(cleaned, 208, 256) {
		t.Error("GPS was not stripped from JPEG")
	}
	if string(cleaned[76:87]) != "TestCamera\x00" {
		t.Error("JPEG other EXIF metadata was not preserved")
	}
}

func TestStripJPEGOctetStreamDetectedByMagic(t *testing.T) {
	var jpeg bytes.Buffer
	jpeg.Write([]byte{0xFF, 0xD8})
	payload := append([]byte("Exif\x00\x00"), buildTiffLE()...)
	jpeg.Write([]byte{0xFF, 0xE1})
	var l [2]byte
	binary.BigEndian.PutUint16(l[:], uint16(2+len(payload)))
	jpeg.Write(l[:])
	jpeg.Write(payload)
	jpeg.Write([]byte{0xFF, 0xD9})

	out := StripImageLocation(jpeg.Bytes(), "application/octet-stream")
	if bytes.Equal(out, jpeg.Bytes()) {
		t.Fatal("expected magic detection to find a JPEG")
	}
}

func pngChunk(typ string, data []byte) []byte {
	var out bytes.Buffer
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(data)))
	out.Write(l[:])
	out.WriteString(typ)
	out.Write(data)
	h := crc32.NewIEEE()
	h.Write([]byte(typ))
	h.Write(data)
	binary.BigEndian.PutUint32(l[:], h.Sum32())
	out.Write(l[:])
	return out.Bytes()
}

func TestStripPNGLocation(t *testing.T) {
	var png bytes.Buffer
	png.Write([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A})
	png.Write(pngChunk("IHDR", make([]byte, 13)))
	png.Write(pngChunk("eXIf", buildTiffLE()))
	png.Write(pngChunk("IEND", nil))

	out := StripImageLocation(png.Bytes(), "image/png")
	if bytes.Equal(out, png.Bytes()) {
		t.Fatal("expected the PNG to be rewritten")
	}
	// Re-scan chunks to find eXIf and verify GPS is zeroed.
	i := 8
	cleanIHDR := true
	cleanExif := false
	for i+8 <= len(out) {
		length := int(binary.BigEndian.Uint32(out[i : i+4]))
		typ := string(out[i+4 : i+8])
		data := out[i+8 : i+8+length]
		switch typ {
		case "IHDR":
			cleanIHDR = bytes.Equal(data, make([]byte, 13))
		case "eXIf":
			cleanExif = zeroed(data, 208, 256) && string(data[76:87]) == "TestCamera\x00"
		}
		i += 8 + length + 4
	}
	if !cleanIHDR {
		t.Error("IHDR chunk was damaged")
	}
	if !cleanExif {
		t.Error("PNG eXIf GPS was not stripped or other metadata lost")
	}
}

func webpChunk(typ string, data []byte) []byte {
	var out bytes.Buffer
	out.WriteString(typ)
	var sz [4]byte
	binary.LittleEndian.PutUint32(sz[:], uint32(len(data)))
	out.Write(sz[:])
	out.Write(data)
	if len(data)%2 == 1 {
		out.WriteByte(0) // WebP chunks are padded to an even size
	}
	return out.Bytes()
}

func TestStripWebPLocation(t *testing.T) {
	exif := buildTiffLE()
	// WebP container: RIFF <size> WEBP <chunks>
	var body bytes.Buffer
	body.Write(webpChunk("VP8 ", make([]byte, 4)))
	body.Write(webpChunk("EXIF", exif))
	body.Write(webpChunk("ICCP", make([]byte, 3)))

	var webp bytes.Buffer
	webp.WriteString("RIFF")
	var sz [4]byte
	binary.LittleEndian.PutUint32(sz[:], uint32(4+body.Len()))
	webp.Write(sz[:])
	webp.WriteString("WEBP")
	webp.Write(body.Bytes())

	out := StripImageLocation(webp.Bytes(), "image/webp")
	if bytes.Equal(out, webp.Bytes()) {
		t.Fatal("expected the WebP to be rewritten")
	}
	if string(out[0:4]) != "RIFF" || string(out[8:12]) != "WEBP" {
		t.Fatal("WebP header was damaged")
	}
	// Find EXIF chunk and verify GPS zeroed.
	rest := out[12:]
	i := 0
	found := false
	for i+8 <= len(rest) {
		typ := string(rest[i : i+4])
		size := int(binary.LittleEndian.Uint32(rest[i+4 : i+8]))
		data := rest[i+8 : i+8+size]
		if typ == "EXIF" {
			found = zeroed(data, 208, 256) && string(data[76:87]) == "TestCamera\x00"
		}
		i += 8 + size
		if size%2 == 1 {
			i++
		}
	}
	if !found {
		t.Error("WebP EXIF GPS was not stripped")
	}
}

func TestStripImageLocationNonImageUnchanged(t *testing.T) {
	data := []byte("plain text file, definitely not an image")
	out := StripImageLocation(data, "text/plain")
	if !bytes.Equal(out, data) {
		t.Fatal("non-image data must be returned unchanged")
	}
}
