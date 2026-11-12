package images

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
)

// 只读取 EXIF 的 IFD0 方向字段，不解析其他元数据或跟随子目录。
func exifOrientation(data []byte) uint16 {
	data = bytes.TrimPrefix(data, []byte("Exif\x00\x00"))
	if len(data) < 8 {
		return 1
	}
	var order binary.ByteOrder
	switch string(data[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1
	}
	if order.Uint16(data[2:4]) != 42 {
		return 1
	}
	pos := uint64(order.Uint32(data[4:8]))
	if pos < 8 || pos+2 > uint64(len(data)) {
		return 1
	}
	count := uint64(order.Uint16(data[pos : pos+2]))
	pos += 2
	if count > (uint64(len(data))-pos)/12 {
		return 1
	}
	for i := uint64(0); i < count; i++ {
		entry := data[pos+i*12 : pos+(i+1)*12]
		if order.Uint16(entry[:2]) == 0x0112 && order.Uint16(entry[2:4]) == 3 && order.Uint32(entry[4:8]) == 1 {
			if value := order.Uint16(entry[8:10]); value >= 1 && value <= 8 {
				return value
			}
		}
	}
	return 1
}

func outputOrientation(data []byte) uint16 {
	if bytes.HasPrefix(data, []byte{0xff, 0xd8}) {
		// JPEG 的 APP1 只有长度字段指定的段可作为 EXIF，不能跨越图像扫描数据。
		for pos := 2; pos+4 <= len(data); {
			if data[pos] != 0xff {
				break
			}
			pos++
			for pos < len(data) && data[pos] == 0xff {
				pos++
			}
			if pos+3 > len(data) || data[pos] == 0xda || data[pos] == 0xd9 {
				break
			}
			marker := data[pos]
			length := int(binary.BigEndian.Uint16(data[pos+1 : pos+3]))
			if length < 2 || length > len(data)-pos-1 {
				break
			}
			segment := data[pos+3 : pos+1+length]
			if marker == 0xe1 && bytes.HasPrefix(segment, []byte("Exif\x00\x00")) {
				return exifOrientation(segment)
			}
			pos += 1 + length
		}
	} else if bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
		for pos := 8; pos+12 <= len(data); {
			length := uint64(binary.BigEndian.Uint32(data[pos : pos+4]))
			if length > uint64(len(data)-pos-12) {
				break
			}
			if string(data[pos+4:pos+8]) == "eXIf" {
				return exifOrientation(data[pos+8 : pos+8+int(length)])
			}
			if string(data[pos+4:pos+8]) == "IEND" {
				break
			}
			pos += int(length) + 12
		}
	} else if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		end := uint64(binary.LittleEndian.Uint32(data[4:8])) + 8
		if end < 12 || end > uint64(len(data)) {
			return 1
		}
		for pos := 12; uint64(pos+8) <= end; {
			length := uint64(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
			if length > end-uint64(pos)-8 {
				break
			}
			if string(data[pos:pos+4]) == "EXIF" {
				return exifOrientation(data[pos+8 : pos+8+int(length)])
			}
			pos += int(length) + int(length&1) + 8
		}
	}
	return 1
}

func orientOutput(src image.Image, data []byte) image.Image {
	orientation := outputOrientation(data)
	if orientation == 1 {
		return src
	}
	return orientedOutput{Image: src, orientation: orientation}
}

// 使用坐标视图，避免为旋转大图再分配一整张 RGBA 缓冲。
type orientedOutput struct {
	image.Image
	orientation uint16
}

func (m orientedOutput) Bounds() image.Rectangle {
	w, h := m.Image.Bounds().Dx(), m.Image.Bounds().Dy()
	if m.orientation >= 5 {
		w, h = h, w
	}
	return image.Rect(0, 0, w, h)
}

func (m orientedOutput) At(x, y int) color.Color {
	b := m.Image.Bounds()
	w, h := b.Dx(), b.Dy()
	switch m.orientation {
	case 2:
		x = w - 1 - x
	case 3:
		x, y = w-1-x, h-1-y
	case 4:
		y = h - 1 - y
	case 5:
		x, y = y, x
	case 6:
		x, y = y, h-1-x
	case 7:
		x, y = w-1-y, h-1-x
	case 8:
		x, y = w-1-y, x
	}
	return m.Image.At(b.Min.X+x, b.Min.Y+y)
}
