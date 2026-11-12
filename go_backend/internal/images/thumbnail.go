package images

import (
	"archive/zip"
	"bytes"
	"context"
	"image"
	"image/color"
	stddraw "image/draw"
	"image/jpeg"
	"io"
	"os"
	"strings"

	"golang.org/x/image/draw"
)

func thumbnailPath(generation string) string { return ".gallery/thumbnails/" + generation + ".jpeg" }

// OpenThumbnailContext 从唯一资产读取入口生成可重建缩略图，远端请求不占目录锁。
func (s *ImageStorageService) OpenThumbnail(p string) (*AssetFile, error) {
	return s.OpenThumbnailContext(context.Background(), p)
}
func (s *ImageStorageService) OpenThumbnailContext(parent context.Context, p string) (*AssetFile, error) {
	if strings.HasSuffix(p, ".svg") {
		return s.OpenContext(parent, p)
	}
	ctx, done, e := s.beginStorage(parent)
	if e != nil {
		return nil, e
	}
	success := false
	defer func() {
		if !success {
			done()
		}
	}()
	lock := s.assetMutex(p)
	lock.Lock()
	defer lock.Unlock()
	entry, e := s.snapshotEntry(p)
	if e != nil {
		return nil, e
	}
	cache := thumbnailPath(entry.Generation)
	s.mu.Lock()
	info, err := s.root.Lstat(cache)
	if err == nil && info.Mode().IsRegular() && info.Size() <= maxAssetBytes {
		file, err := s.root.Open(cache)
		s.mu.Unlock()
		if err != nil {
			return nil, ErrAssets
		}
		out := &AssetFile{file: file, done: done}
		context.AfterFunc(ctx, func() { out.Close() })
		success = true
		return out, nil
	}
	s.mu.Unlock()
	if err != nil && !os.IsNotExist(err) {
		return nil, ErrAssets
	}
	data, e := s.bytesForEntry(ctx, p, entry)
	if e != nil {
		return nil, e
	}
	s.decodeMu.Lock()
	defer s.decodeMu.Unlock()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	original, _, e := decodeImage(data)
	if e != nil {
		return nil, ErrAssets
	}
	w, h := original.Bounds().Dx(), original.Bounds().Dy()
	if w > 512 || h > 512 {
		longest := max(w, h)
		w = max(1, w*512/longest)
		h = max(1, h*512/longest)
	}
	target := image.NewRGBA(image.Rect(0, 0, w, h))
	stddraw.Draw(target, target.Bounds(), image.NewUniform(color.White), image.Point{}, stddraw.Src)
	draw.CatmullRom.Scale(target, target.Bounds(), original, original.Bounds(), stddraw.Over, nil)
	var encoded bytes.Buffer
	if jpeg.Encode(&encoded, target, &jpeg.Options{Quality: 85}) != nil {
		return nil, ErrAssets
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	s.mu.Lock()
	current, ok := s.catalog.Entries[p]
	_, pending := s.catalog.Tombstones[p]
	if !ok || pending || current.Generation != entry.Generation {
		s.mu.Unlock()
		return nil, ErrAssets
	}
	if e = s.atomicWrite(cache, encoded.Bytes()); e != nil {
		s.mu.Unlock()
		return nil, e
	}
	file, e := s.root.Open(cache)
	s.mu.Unlock()
	if e != nil {
		return nil, ErrAssets
	}
	out := &AssetFile{file: file, done: done}
	context.AfterFunc(ctx, func() { out.Close() })
	if ctx.Err() != nil {
		out.Close()
		return nil, ctx.Err()
	}
	success = true
	return out, nil
}

type galleryContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r galleryContextReader) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.r.Read(p)
}

func (s *ImageStorageService) DownloadZip(parent context.Context, paths []string) ([]byte, error) {
	paths, e := validatePaths(paths, 100)
	if e != nil {
		return nil, e
	}
	ctx, done, e := s.beginStorage(parent)
	if e != nil {
		return nil, e
	}
	defer done()
	entries := map[string]catalogEntry{}
	var total int64
	s.mu.Lock()
	for _, p := range paths {
		entry, exists := s.catalog.Entries[p]
		_, pending := s.catalog.Tombstones[p]
		if !exists || pending {
			s.mu.Unlock()
			return nil, ErrGalleryInput
		}
		total += entry.Size
		if total > 100<<20 {
			s.mu.Unlock()
			return nil, ErrGalleryInput
		}
		entries[p] = entry
	}
	s.mu.Unlock()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, p := range paths {
		lock := s.assetMutex(p)
		lock.Lock()
		current, e := s.snapshotEntry(p)
		if e == nil && current.Generation != entries[p].Generation {
			e = ErrAssets
		}
		var data []byte
		if e == nil {
			data, e = s.bytesForEntry(ctx, p, current)
		}
		lock.Unlock()
		if e != nil {
			writer.Close()
			return nil, e
		}
		header := &zip.FileHeader{Name: p, Method: zip.Store}
		header.SetMode(0600)
		header.SetModTime(current.CreatedAt)
		item, e := writer.CreateHeader(header)
		if e != nil {
			writer.Close()
			return nil, ErrAssets
		}
		if _, e = io.Copy(item, galleryContextReader{ctx: ctx, r: bytes.NewReader(data)}); e != nil {
			writer.Close()
			return nil, e
		}
	}
	if writer.Close() != nil {
		return nil, ErrAssets
	}
	return buffer.Bytes(), nil
}
