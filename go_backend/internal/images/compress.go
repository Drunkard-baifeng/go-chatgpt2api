package images

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"sort"
	"strings"
)

type ImageCompressResult struct {
	Compressed int    `json:"compressed"`
	SavedBytes int64  `json:"saved_bytes"`
	SavedMB    int64  `json:"saved_mb"`
	Message    string `json:"message"`
}

// Compress 保持 PNG 编码，更新资产代次并使旧缩略图和外部回执失效。
func (s *ImageStorageService) Compress(parent context.Context) (ImageCompressResult, error) {
	ctx, done, e := s.beginStorage(parent)
	if e != nil {
		return ImageCompressResult{}, e
	}
	defer done()
	cfg, e := s.storageConfig(ctx)
	if e != nil {
		return ImageCompressResult{}, e
	}
	s.mu.Lock()
	paths := []string{}
	for p, entry := range s.catalog.Entries {
		_, pending := s.catalog.Tombstones[p]
		if !pending && hasLocal(entry) && strings.HasSuffix(p, ".png") {
			paths = append(paths, p)
		}
	}
	s.mu.Unlock()
	sort.Strings(paths)
	out := ImageCompressResult{}
	for _, p := range paths {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		lock := s.assetMutex(p)
		lock.Lock()
		saved, e := s.compressEntry(ctx, p, cfg)
		lock.Unlock()
		if e != nil {
			return out, e
		}
		if saved > 0 {
			out.Compressed++
			out.SavedBytes += saved
		}
	}
	out.SavedMB = out.SavedBytes >> 20
	out.Message = fmt.Sprintf("已压缩 %d 张图片，节省 %d 字节。", out.Compressed, out.SavedBytes)
	return out, nil
}
func (s *ImageStorageService) compressEntry(ctx context.Context, p string, cfg storageConfiguration) (int64, error) {
	entry, e := s.snapshotEntry(p)
	if e != nil {
		return 0, nil
	}
	data, e := s.bytesForEntry(ctx, p, entry)
	if e != nil {
		return 0, e
	}
	s.decodeMu.Lock()
	if ctx.Err() != nil {
		s.decodeMu.Unlock()
		return 0, ctx.Err()
	}
	decoded, format, e := image.Decode(bytes.NewReader(data))
	if e != nil || format != "png" {
		s.decodeMu.Unlock()
		return 0, ErrAssets
	}
	var encoded bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.BestCompression}
	e = encoder.Encode(&encoded, decoded)
	s.decodeMu.Unlock()
	if e != nil {
		return 0, ErrAssets
	}
	if encoded.Len() >= len(data) {
		return 0, nil
	}
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	generation, e := randomID()
	if e != nil {
		return 0, e
	}
	s.mu.Lock()
	current, ok := s.catalog.Entries[p]
	_, pending := s.catalog.Tombstones[p]
	if !ok || pending || current.Generation != entry.Generation {
		s.mu.Unlock()
		return 0, ErrAssets
	}
	if e = s.writeAssetLocked(p, encoded.Bytes()); e != nil {
		s.mu.Unlock()
		return 0, e
	}
	current.Generation = generation
	current.Size = int64(encoded.Len())
	current.SHA256 = assetHash(encoded.Bytes())
	current.Genbox = nil
	if current.Remote != nil {
		remote := *current.Remote
		remote.Pending = true
		current.Remote = &remote
	}
	next := s.catalog.clone()
	next.Entries[p] = current
	e = s.commit(next)
	if e == nil {
		_ = s.root.Remove(thumbnailPath(entry.Generation))
	}
	s.mu.Unlock()
	if e != nil {
		return 0, e
	}
	if current.Remote != nil {
		if e = s.syncEntry(ctx, p, generation, cfg); e != nil {
			return 0, e
		}
	}
	return int64(len(data) - encoded.Len()), nil
}
