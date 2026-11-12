package images

import (
	"bytes"
	"context"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"

	"chatgpt2api/go_backend/internal/assettransfer"
	"chatgpt2api/go_backend/internal/callrecord"
)

// processOutput 在同一解码锁内完成方向修正、可选放大和最终编码。
// 无需改变的图片直接保留原字节；放大失败尽力保留未放大的请求格式，取消则不发布。
func (s *ImageStorageService) processOutput(ctx context.Context, data []byte, format, requestedSize string, upscale bool) (result []byte, resultErr error) {
	var done func(error)
	defer func() {
		if err := ctx.Err(); err != nil {
			result, resultErr = nil, err
		}
		if done != nil {
			done(resultErr)
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if format == "gif" || format == "svg" {
		input, err := Inspect(data)
		if err != nil || (format == "svg" && input.Media != "image/svg+xml") {
			return nil, ErrOutput
		}
		return data, nil
	}
	if format != "" && format != "png" && format != "jpeg" && format != "webp" {
		return nil, ErrInput
	}
	target := image.Point{}
	if upscale {
		target = upscaleTarget(requestedSize)
	}
	if format == "" && target.X == 0 {
		return data, nil
	}
	cfg, kind, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || len(data) > assettransfer.MaxBytes || cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width)*int64(cfg.Height) > maxUpscalePixels {
		if format == "" {
			return data, nil
		}
		return nil, ErrOutput
	}
	static := kind == "png" || kind == "jpeg" || kind == "webp"
	if !static && format == "" {
		return data, nil
	}
	orientation := outputOrientation(data)
	width, height := cfg.Width, cfg.Height
	// 指定格式时沿用原先先校正方向再比较尺寸的行为；未指定时按原始编码尺寸比较。
	if format != "" && orientation >= 5 {
		width, height = height, width
	}
	needsResize := target.X > 0 && (static || kind == "gif") && (width < target.X || height < target.Y)
	needsConvert := format != "" && (format != kind || orientation != 1)
	if !needsConvert && !needsResize {
		return data, nil
	}
	done = callrecord.Span(ctx, "image_processing", "processing_ms")
	if err := s.lockOutputDecode(ctx); err != nil {
		return nil, err
	}
	defer s.decodeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	decoded, _, err := decodeImage(data)
	if err != nil {
		if format == "" {
			return data, nil
		}
		return nil, ErrOutput
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	decoded = orientOutput(decoded, data)
	original := decoded
	resized := false
	if needsResize {
		decoded, err = resizeOutputLanczos(original, target.X, target.Y)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err != nil {
			if !needsConvert {
				return data, nil
			}
			decoded = original
		} else {
			resized = true
		}
	}
	outputKind := format
	if outputKind == "" {
		outputKind = kind
	}
	compression := png.BestCompression
	if resized {
		compression = png.DefaultCompression
	}
	result, resultErr = encodeOutput(ctx, decoded, outputKind, compression)
	if resultErr != nil && resized && ctx.Err() == nil {
		if !needsConvert {
			return data, nil
		}
		// 仅放大结果无法编码时回退：仍保证请求格式和方向正确，不重试生成。
		return encodeOutput(ctx, original, outputKind, png.BestCompression)
	}
	return result, resultErr
}

func encodeOutput(ctx context.Context, decoded image.Image, format string, compression png.CompressionLevel) ([]byte, error) {
	w := &imageOutputWriter{ctx: ctx}
	var err error
	switch format {
	case "png":
		encoder := png.Encoder{CompressionLevel: compression}
		err = encoder.Encode(w, decoded)
	case "jpeg":
		// 与 Python 一致，透明区域合成白底后再输出 JPEG。
		background := image.NewRGBA(decoded.Bounds())
		draw.Draw(background, background.Bounds(), image.White, image.Point{}, draw.Src)
		draw.Draw(background, background.Bounds(), decoded, decoded.Bounds().Min, draw.Over)
		err = jpeg.Encode(w, background, &jpeg.Options{Quality: 95})
	case "webp":
		err = encodeWebP(ctx, w, decoded)
	default:
		return nil, ErrInput
	}
	// 编码过程及最后一次写入都必须尊重调用预算。
	if w.err != nil {
		return nil, w.err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || w.buffer.Len() == 0 {
		return nil, ErrOutput
	}
	return w.buffer.Bytes(), nil
}

type imageOutputWriter struct {
	ctx    context.Context
	buffer bytes.Buffer
	err    error
}

func (w *imageOutputWriter) Write(p []byte) (int, error) {
	if w.err == nil {
		w.err = w.ctx.Err()
	}
	if w.err == nil && len(p) > assettransfer.MaxBytes-w.buffer.Len() {
		w.err = ErrOutput
	}
	if w.err != nil {
		return 0, w.err
	}
	return w.buffer.Write(p)
}
