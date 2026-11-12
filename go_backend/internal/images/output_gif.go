package images

import "encoding/binary"

// Validate frame descriptors before DecodeAll allocates animation pixels. A
// compressed animation has the same total decoded-pixel budget as a raster.
func boundedGIF(data []byte) bool {
	if len(data) < 13 || (string(data[:6]) != "GIF87a" && string(data[:6]) != "GIF89a") {
		return false
	}
	offset := 13
	if data[10]&0x80 != 0 {
		offset += 3 * (1 << (1 + (data[10] & 7)))
	}
	frames, pixels := 0, int64(0)
	skipBlocks := func() bool {
		for offset < len(data) {
			size := int(data[offset])
			offset++
			if size == 0 {
				return true
			}
			if size > len(data)-offset {
				return false
			}
			offset += size
		}
		return false
	}
	for offset < len(data) {
		marker := data[offset]
		offset++
		switch marker {
		case 0x3b:
			return frames > 0
		case 0x21:
			if offset >= len(data) {
				return false
			}
			offset++ // extension label
			if !skipBlocks() {
				return false
			}
		case 0x2c:
			if len(data)-offset < 9 {
				return false
			}
			width := int64(binary.LittleEndian.Uint16(data[offset+4:]))
			height := int64(binary.LittleEndian.Uint16(data[offset+6:]))
			frames++
			pixels += width * height
			if width == 0 || height == 0 || frames > 1024 || pixels > 40_000_000 {
				return false
			}
			packed := data[offset+8]
			offset += 9
			if packed&0x80 != 0 {
				offset += 3 * (1 << (1 + (packed & 7)))
			}
			if offset >= len(data) {
				return false
			}
			offset++ // LZW minimum code size
			if !skipBlocks() {
				return false
			}
		default:
			return false
		}
	}
	return false
}
