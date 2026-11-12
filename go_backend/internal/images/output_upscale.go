package images

import (
	"context"
	"image"
	"image/color"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/disintegration/imaging"
)

const maxUpscalePixels = 40_000_000

var upscaleSizePattern = regexp.MustCompile(`^(\d{2,5})\s*x\s*(\d{2,5})$`)

func upscaleTarget(value string) image.Point {
	match := upscaleSizePattern.FindStringSubmatch(strings.ToLower(strings.TrimSpace(value)))
	if match == nil {
		return image.Point{}
	}
	w, _ := strconv.Atoi(match[1])
	h, _ := strconv.Atoi(match[2])
	if w < 1 || h < 1 || w > 8192 || h > 8192 || int64(w)*int64(h) > maxUpscalePixels {
		return image.Point{}
	}
	return image.Pt(w, h)
}

// Wait without a helper goroutine, so cancellation also works while another
// image owns the shared decoder and encoder memory budget.
func (s *ImageStorageService) lockOutputDecode(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for !s.decodeMu.TryLock() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	return nil
}

// Both input and output must already be bounded to 40 Mp by the caller. imaging
// normally resizes horizontally first; transpose views choose the smaller
// intermediate image, whose pixel count cannot exceed the larger endpoint.
// Resize joins its workers before returning. It cannot be interrupted mid-call;
// callers hold decodeMu and check context before and after this bounded work.
func resizeOutputLanczos(src image.Image, width, height int) (image.Image, error) {
	bounds := src.Bounds()
	if width < 1 || height < 1 || width > maxUpscalePixels || height > maxUpscalePixels ||
		bounds.Dx() < 1 || bounds.Dy() < 1 || bounds.Dx() > maxUpscalePixels || bounds.Dy() > maxUpscalePixels ||
		int64(width)*int64(height) > maxUpscalePixels || int64(bounds.Dx())*int64(bounds.Dy()) > maxUpscalePixels {
		return nil, ErrOutput
	}
	// imaging precomputes a six-tap table (wider when reducing). A 40 Mp
	// one-pixel strip otherwise has a multi-GiB coefficient allocation despite
	// its bounded raster. Reject work whose two tables could exceed 64 MiB.
	weightsBytes := func(dst, source int) int64 {
		support := int64(math.Ceil(3 * math.Max(float64(source)/float64(dst), 1)))
		return int64(dst) * ((support+2)*2*16 + 24)
	}
	if weightsBytes(width, bounds.Dx())+weightsBytes(height, bounds.Dy()) > 64<<20 {
		return nil, ErrOutput
	}
	if int64(width)*int64(bounds.Dy()) <= int64(height)*int64(bounds.Dx()) {
		return imaging.Resize(src, width, height, imaging.Lanczos), nil
	}
	return transposedOutput{Image: imaging.Resize(transposedOutput{Image: src}, height, width, imaging.Lanczos)}, nil
}

type transposedOutput struct{ image.Image }

func (m transposedOutput) Bounds() image.Rectangle {
	return image.Rect(0, 0, m.Image.Bounds().Dy(), m.Image.Bounds().Dx())
}

func (m transposedOutput) At(x, y int) color.Color {
	b := m.Image.Bounds()
	return m.Image.At(b.Min.X+y, b.Min.Y+x)
}
