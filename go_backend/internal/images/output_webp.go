package images

import (
	"bytes"
	"compress/gzip"
	"context"
	_ "embed"
	"image"
	"image/draw"
	"io"

	"chatgpt2api/go_backend/internal/assettransfer"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// 固定的 libwebp 资源及许可证来源见 codec/README.md。
//
//go:embed codec/webp.wasm.gz
var webpWasm []byte

// 只限制编解码器的线性内存；原始图片仍受共享解码锁及像素上限约束。
const webPMemoryPages = 8192 // 512 MiB，每页 64 KiB。

// encodeWebP 使用独立、可取消的实例。关闭实例会释放本次编码的全部内存。
func encodeWebP(ctx context.Context, w io.Writer, src image.Image) error {
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().
		WithMemoryLimitPages(webPMemoryPages).WithCloseOnContextDone(true))
	defer rt.Close(context.Background())
	compressed, err := gzip.NewReader(bytes.NewReader(webpWasm))
	if err != nil {
		return ErrOutput
	}
	code, err := io.ReadAll(io.LimitReader(compressed, (4<<20)+1))
	compressed.Close()
	if err != nil || len(code) > 4<<20 {
		return ErrOutput
	}
	if _, err = wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		return ErrOutput
	}
	mod, err := rt.Instantiate(ctx, code)
	if err != nil {
		return ErrOutput
	}
	alloc, encode := mod.ExportedFunction("malloc"), mod.ExportedFunction("encode")
	if alloc == nil || encode == nil || mod.Memory() == nil {
		return ErrOutput
	}
	width, height := src.Bounds().Dx(), src.Bounds().Dy()
	if width < 1 || height < 1 || int64(width)*int64(height) > 40_000_000 {
		return ErrOutput
	}
	// 前 8 字节接收 size_t；其后是紧密排列、非预乘的 RGBA。
	pointers, err := alloc.Call(ctx, uint64(width)*uint64(height)*4+8)
	if err != nil || len(pointers) != 1 || pointers[0] == 0 {
		return ErrOutput
	}
	sizePtr := uint32(pointers[0])
	inPtr := sizePtr + 8
	row := image.NewNRGBA(image.Rect(0, 0, width, 1))
	for y := 0; y < height; y++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		draw.Draw(row, row.Bounds(), src, src.Bounds().Min.Add(image.Pt(0, y)), draw.Src)
		if !mod.Memory().Write(inPtr+uint32(y*width*4), row.Pix) {
			return ErrOutput
		}
	}
	// 与 Python Pillow 的 WebP 参数一致：quality=95、method=4、有损编码。
	out, err := encode.Call(ctx, uint64(inPtr), uint64(width), uint64(height), uint64(sizePtr), 0, 95, 4, 0, 0)
	if err != nil || len(out) != 1 || out[0] == 0 {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrOutput
	}
	// 此固定资源为 wasm32，size_t 是 32 位。
	size, ok := mod.Memory().ReadUint32Le(sizePtr)
	if !ok || size == 0 || size > assettransfer.MaxBytes {
		return ErrOutput
	}
	data, ok := mod.Memory().Read(uint32(out[0]), uint32(size))
	if !ok {
		return ErrOutput
	}
	_, err = w.Write(data)
	return err
}
