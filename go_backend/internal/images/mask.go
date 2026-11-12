package images

import (
	"context"
	"image"
	"image/color"
	"image/png"
)

// CompositeMasks applies the edit mask after both input groups were validated.
func CompositeMasks(ctx context.Context, inputs, masks []Input) ([]Input, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(masks) == 0 {
		return inputs, nil
	}
	if len(inputs) == 0 || len(inputs) > MaxInputs || len(masks) > MaxInputs {
		return nil, ErrInput
	}
	total := 0
	for _, group := range [][]Input{inputs, masks} {
		for _, input := range group {
			total += len(input.Data)
		}
	}
	if total > MaxInputBytes {
		return nil, ErrInput
	}
	outputs := make([]Input, 0, len(inputs))
	outputBytes := 0
	for i, input := range inputs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		source, _, err := decodeImage(input.Data)
		if err != nil {
			return nil, err
		}
		maskInput := masks[min(i, len(masks)-1)]
		mask, kind, err := decodeImage(maskInput.Data)
		if err != nil {
			return nil, err
		}
		alpha, err := maskAlpha(ctx, mask, maskInput.Data, kind)
		if err != nil {
			return nil, err
		}
		bounds := source.Bounds()
		if alpha.Bounds().Dx() != bounds.Dx() || alpha.Bounds().Dy() != bounds.Dy() {
			alpha, err = resizeOutputLanczos(alpha, bounds.Dx(), bounds.Dy())
			if err != nil {
				return nil, ErrInput
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		composite := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
		for y := 0; y < bounds.Dy(); y++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			for x := 0; x < bounds.Dx(); x++ {
				pixel := color.NRGBAModel.Convert(source.At(bounds.Min.X+x, bounds.Min.Y+y)).(color.NRGBA)
				pixel.A = color.NRGBAModel.Convert(alpha.At(x, y)).(color.NRGBA).R
				composite.SetNRGBA(x, y, pixel)
			}
		}
		writer := &imageOutputWriter{ctx: ctx}
		if err := png.Encode(writer, composite); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, ErrInput
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data := writer.buffer.Bytes()
		outputBytes += len(data)
		if outputBytes > MaxInputBytes {
			return nil, ErrInput
		}
		outputs = append(outputs, Input{Data: data, Media: "image/png", Name: input.Name, Width: bounds.Dx(), Height: bounds.Dy()})
	}
	return outputs, nil
}

// Pillow uses the alpha of an RGBA mask, the values of an L mask, and RGB
// luminance for other modes. PNG's encoded color type distinguishes palette
// transparency and LA from RGBA, even when Go decodes them to the same type.
func maskAlpha(ctx context.Context, mask image.Image, data []byte, kind string) (image.Image, error) {
	usesAlpha := false
	if kind == "png" && len(data) > 25 {
		usesAlpha = data[25] == 6
	} else if kind == "webp" {
		switch mask.(type) {
		case *image.NRGBA, *image.NRGBA64:
			usesAlpha = true
		}
	}
	bounds := mask.Bounds()
	alpha := image.NewGray(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	for y := 0; y < bounds.Dy(); y++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for x := 0; x < bounds.Dx(); x++ {
			pixel := color.NRGBAModel.Convert(mask.At(bounds.Min.X+x, bounds.Min.Y+y)).(color.NRGBA)
			gray := uint8((19595*uint32(pixel.R) + 38470*uint32(pixel.G) + 7471*uint32(pixel.B) + 32768) >> 16)
			if usesAlpha {
				gray = pixel.A
			} else if m, ok := mask.(*image.Gray16); ok {
				gray = uint8(min(m.Gray16At(bounds.Min.X+x, bounds.Min.Y+y).Y, 255))
			}
			alpha.SetGray(x, y, color.Gray{Y: gray})
		}
	}
	return alpha, nil
}
