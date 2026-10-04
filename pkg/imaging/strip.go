// Package imaging sanitizes uploaded images: it removes location metadata
// from JPEG, PNG and WebP bytes while preserving all other EXIF tags.
package imaging

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"math"
	"strings"
)

// StripImageLocation removes GPS / location metadata from image bytes while
// preserving the rest of the metadata (orientation, camera model, date, ...).
// Supported containers:
//
//   - JPEG: zeros the GPS IFD (tag 0x8825) inside the EXIF APP1 segment. The
//     TIFF layout is kept intact so every remaining offset stays valid and all
//     other EXIF tags are preserved verbatim.
//   - PNG: zeros the GPS IFD inside the eXIf chunk.
//   - WebP: zeros the GPS IFD inside the EXIF chunk.
//
// Anything that is not a supported image, carries no location metadata, or
// cannot be parsed is returned unchanged. Sanitization therefore never breaks
// an upload.
func StripImageLocation(data []byte, contentType string) []byte {
	if len(data) < 16 {
		return data
	}
	switch {
	case isJPEG(data, contentType):
		return stripJPEGLocation(data)
	case isPNG(data):
		return stripPNGLocation(data)
	case isWebP(data):
		return stripWebPLocation(data)
	default:
		return data
	}
}

func isJPEG(data []byte, contentType string) bool {
	if len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 {
		return true
	}
	t := strings.ToLower(contentType)
	return strings.Contains(t, "jpeg") || strings.Contains(t, "jpg")
}

func isPNG(data []byte) bool {
	return len(data) >= 8 &&
		data[0] == 0x89 && data[1] == 'P' && data[2] == 'N' && data[3] == 'G' &&
		data[4] == 0x0D && data[5] == 0x0A && data[6] == 0x1A && data[7] == 0x0A
}

func isWebP(data []byte) bool {
	return len(data) >= 12 &&
		bytes.Equal(data[0:4], []byte("RIFF")) &&
		bytes.Equal(data[8:12], []byte("WEBP"))
}

// stripJPEGLocation rewrites a JPEG stream with the EXIF APP1 segment modified
// so the GPS IFD is zeroed. Non-EXIF segments are copied verbatim.
func stripJPEGLocation(data []byte) []byte {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return data
	}
	out := make([]byte, 0, len(data))
	out = append(out, data[0], data[1])
	modified := false
	i := 2
	for i < len(data) {
		if i+1 >= len(data) || data[i] != 0xFF {
			out = append(out, data[i:]...)
			break
		}
		marker := data[i+1]
		// Standalone markers carry no length field.
		if marker == 0x01 || marker == 0xD8 || (marker >= 0xD0 && marker <= 0xD7) {
			out = append(out, data[i], data[i+1])
			i += 2
			continue
		}
		if i+3 > len(data) {
			out = append(out, data[i:]...)
			break
		}
		segLen := int(binary.BigEndian.Uint16(data[i+2 : i+4]))
		// segLen counts the two length bytes, so the payload spans
		// [i+4, i+2+segLen). Both offsets are computed in uint64 to keep the
		// addition from overflowing the int range of a 32-bit platform.
		payloadStart := uint64(i) + 4
		segEnd64 := uint64(i) + 2 + uint64(segLen)
		if segLen < 2 || segEnd64 > uint64(len(data)) {
			out = append(out, data[i:]...)
			break
		}
		segEnd := int(segEnd64)
		payload := data[payloadStart:segEnd]
		if marker == 0xE1 && bytes.HasPrefix(payload, []byte("Exif\x00\x00")) {
			clean := stripExifGPS(payload[6:])
			if !bytes.Equal(clean, payload[6:]) {
				modified = true
				payload = append(append([]byte{}, payload[:6]...), clean...)
			}
		}
		out = append(out, data[i], data[i+1])
		out = appendSegment(out, payload)
		i = segEnd
	}
	if !modified {
		return data
	}
	return out
}

// appendSegment writes a JPEG segment payload with its length prefix. The
// length counts the two length bytes plus the payload. The caller validates
// payload against the 16-bit length field before calling, and this function
// keeps the invariant even if it does not: a malformed outcome is dropped
// rather than written as a wrong length.
func appendSegment(out []byte, payload []byte) []byte {
	if uint64(len(payload))+2 > math.MaxUint16 {
		return out
	}
	var l [2]byte
	binary.BigEndian.PutUint16(l[:], uint16(2+len(payload)))
	out = append(out, l[:]...)
	return append(out, payload...)
}

// stripPNGLocation rewrites a PNG stream, zeroing the GPS IFD inside the
// eXIf chunk if present.
func stripPNGLocation(data []byte) []byte {
	if !isPNG(data) {
		return data
	}
	sig := data[:8]
	rest := data[8:]
	out := make([]byte, 0, len(data))
	out = append(out, sig...)
	modified := false
	i := 0
	for i+8 <= len(rest) {
		length64 := uint64(binary.BigEndian.Uint32(rest[i : i+4]))
		typ := string(rest[i+4 : i+8])
		// The chunk payload spans [i+8, i+8+length64), so the whole chunk
		// including its trailing CRC ends at i+12+length64. Both sums are
		// computed in uint64: on a 32-bit platform the conversion of a
		// length above math.MaxInt32 would otherwise wrap to a negative int
		// and make the slice bounds panics below fail to trigger.
		start64 := uint64(i) + 8
		chunkEnd := start64 + length64 + 4 // payload + CRC
		if chunkEnd > uint64(len(rest)) {
			// Truncated chunk; copy the remainder as-is.
			out = append(out, rest[i:]...)
			break
		}
		start, end := int(start64), int(start64+length64)
		chunkData := rest[start:end]
		if typ == "eXIf" {
			clean := stripExifGPS(chunkData)
			if !bytes.Equal(clean, chunkData) {
				modified = true
				chunkData = clean
			}
		}
		out = appendChunk(out, uint32(len(chunkData)), typ, chunkData)
		i = int(chunkEnd) // skip chunk data + CRC
	}
	if !modified {
		return data
	}
	return out
}

func appendChunk(out []byte, length uint32, typ string, data []byte) []byte {
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], length)
	out = append(out, l[:]...)
	out = append(out, typ...)
	out = append(out, data...)
	h := crc32.NewIEEE()
	h.Write([]byte(typ))
	h.Write(data)
	var crc [4]byte
	binary.BigEndian.PutUint32(crc[:], h.Sum32())
	return append(out, crc[:]...)
}

// stripWebPLocation rewrites a WebP stream, zeroing the GPS IFD inside the
// EXIF chunk if present.
func stripWebPLocation(data []byte) []byte {
	if !isWebP(data) {
		return data
	}
	// data: "RIFF" <size:4> "WEBP" <chunks...>
	if len(data) < 20 {
		return data
	}
	out := make([]byte, 0, len(data))
	out = append(out, data[:12]...)
	rest := data[12:]
	modified := false
	i := 0
	for i+8 <= len(rest) {
		fourCC := string(rest[i : i+4])
		size64 := uint64(binary.LittleEndian.Uint32(rest[i+4 : i+8]))
		// The chunk payload is declared to span [i+8, i+8+size64). The sum is
		// computed in uint64 so a size above math.MaxInt32 cannot wrap into a
		// negative int on a 32-bit platform and slip past the bounds check.
		start64 := uint64(i) + 8
		end64 := start64 + size64
		if end64 > uint64(len(rest)) {
			out = append(out, rest[i:]...)
			break
		}
		start, end := int(start64), int(end64)
		chunkData := rest[start:end]
		if fourCC == "EXIF" {
			clean := stripExifGPS(chunkData)
			if !bytes.Equal(clean, chunkData) {
				modified = true
				chunkData = clean
			}
		}
		out = append(out, fourCC...)
		var sz [4]byte
		binary.LittleEndian.PutUint32(sz[:], uint32(len(chunkData)))
		out = append(out, sz[:]...)
		out = append(out, chunkData...)
		// WebP chunks are padded to an even size; preserve the padding byte.
		if len(chunkData)%2 == 1 && end < len(rest) {
			out = append(out, rest[end])
		}
		i = end
		if size64%2 == 1 {
			i++
		}
	}
	if !modified {
		return data
	}
	return out
}

// exifTypeSize returns the byte size of a single TIFF type value.
func exifTypeSize(t uint16) int {
	switch t {
	case 1, 2, 7: // BYTE, ASCII, UNDEFINED
		return 1
	case 3: // SHORT
		return 2
	case 4, 9: // LONG, SLONG
		return 4
	case 5, 10: // RATIONAL, SRATIONAL
		return 8
	default:
		return 0
	}
}

// stripExifGPS returns a copy of a TIFF/EXIF buffer with every value in the
// GPS IFD zeroed. The buffer is copied first so the caller can compare the
// result against the original to detect a change. When the structure cannot be
// parsed the original bytes are returned unchanged.
func stripExifGPS(tiff []byte) []byte {
	clean := make([]byte, len(tiff))
	copy(clean, tiff)
	if len(clean) < 8 {
		return tiff
	}
	var bo binary.ByteOrder
	switch {
	case clean[0] == 'I' && clean[1] == 'I':
		bo = binary.LittleEndian
	case clean[0] == 'M' && clean[1] == 'M':
		bo = binary.BigEndian
	default:
		return tiff
	}
	if bo.Uint16(clean[2:4]) != 42 {
		return tiff
	}
	ifdOffset := int(bo.Uint32(clean[4:8]))
	gpsOffset, ok := findTagInIFD(clean, bo, ifdOffset, 0x8825)
	if !ok {
		return tiff
	}
	zeroGPSIFD(clean, bo, gpsOffset)
	return clean
}

// findTagInIFD returns the value stored by the tag inside the IFD at offset.
// Tags of interest (0x8825 GPS IFD, 0x8769 Exif IFD) store an absolute TIFF
// offset as their value.
func findTagInIFD(tiff []byte, bo binary.ByteOrder, ifdOffset int, tag uint16) (int, bool) {
	if ifdOffset+2 > len(tiff) {
		return 0, false
	}
	count := int(bo.Uint16(tiff[ifdOffset : ifdOffset+2]))
	if count > 512 { // sanity bound
		return 0, false
	}
	entriesStart := ifdOffset + 2
	if entriesStart+count*12 > len(tiff) {
		return 0, false
	}
	for i := 0; i < count; i++ {
		off := entriesStart + i*12
		if uint64(off)+12 > uint64(len(tiff)) {
			// Defensive: the entriesStart/count check above already excludes
			// this, but never let an offset run past the buffer.
			return 0, false
		}
		if bo.Uint16(tiff[off:off+2]) == tag {
			// The stored value is an absolute TIFF offset inside this buffer.
			// Reject anything that does not resolve to a usable position
			// instead of handing a 32-bit value to int arithmetic.
			value := uint64(bo.Uint32(tiff[off+8 : off+12]))
			if value >= uint64(len(tiff)) {
				return 0, false
			}
			return int(value), true
		}
	}
	return 0, false
}

// zeroGPSIFD walks the GPS IFD at offset and zeroes every value, both inline
// values (total size <= 4) and values stored at offsets.
func zeroGPSIFD(tiff []byte, bo binary.ByteOrder, gpsOffset int) {
	if gpsOffset < 0 || gpsOffset+2 > len(tiff) {
		return
	}
	count := int(bo.Uint16(tiff[gpsOffset : gpsOffset+2]))
	if count > 512 { // sanity bound
		return
	}
	entriesStart := gpsOffset + 2
	if uint64(entriesStart)+uint64(count)*12 > uint64(len(tiff)) {
		return
	}
	for i := 0; i < count; i++ {
		off := entriesStart + i*12
		if uint64(off)+12 > uint64(len(tiff)) {
			return
		}
		typ := bo.Uint16(tiff[off+2 : off+4])
		valueCount := uint64(bo.Uint32(tiff[off+4 : off+8]))
		size := uint64(exifTypeSize(typ))
		if size == 0 {
			continue
		}
		// Both factors are uint64, so the product cannot overflow on any
		// platform; valueCount may legitimately be up to 2^32-1.
		total := valueCount * size
		if total == 0 {
			continue
		}
		if total <= 4 {
			// Value stored inline in the 4-byte field.
			clearBytes(tiff, off+8, off+8+int(total))
			continue
		}
		// Value stored at an absolute TIFF offset. The offset is untrusted
		// 32-bit data, so it is range-checked before it becomes an int, and a
		// value that does not fit in the buffer is clamped rather than
		// wrapped; clearBytes re-validates the final range.
		dataOff := uint64(bo.Uint32(tiff[off+8 : off+12]))
		if dataOff >= uint64(len(tiff)) {
			continue
		}
		from := int(dataOff)
		to := len(tiff)
		if end, ok := intAdd(from, total); ok && end < to {
			to = end
		}
		clearBytes(tiff, from, to)
	}
}

// intAdd adds a non-negative int and a uint64 length, reporting whether the
// sum is representable as a non-negative int. It never overflows: the sums are
// formed in uint64, which is wider than int on every supported platform.
func intAdd(a int, b uint64) (int, bool) {
	if a < 0 || b > math.MaxInt64 {
		return 0, false
	}
	sum := uint64(a) + b
	if sum > uint64(math.MaxInt) {
		return 0, false
	}
	return int(sum), true
}

func clearBytes(b []byte, from, to int) {
	if from < 0 || to < 0 || from >= to {
		return
	}
	if from >= len(b) {
		return
	}
	if to > len(b) {
		to = len(b)
	}
	for i := from; i < to; i++ {
		b[i] = 0
	}
}
