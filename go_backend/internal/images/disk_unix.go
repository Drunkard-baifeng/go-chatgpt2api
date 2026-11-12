//go:build !windows

package images

import (
	"golang.org/x/sys/unix"
	"os"
)

func diskSpace(root *os.Root) (int64, int64, error) {
	f, e := root.Open(".")
	if e != nil {
		return 0, 0, ErrAssets
	}
	defer f.Close()
	var stat unix.Statfs_t
	if unix.Fstatfs(int(f.Fd()), &stat) != nil {
		return 0, 0, ErrAssets
	}
	total := uint64(stat.Blocks) * uint64(stat.Bsize)
	free := uint64(stat.Bavail) * uint64(stat.Bsize)
	if total > 1<<63-1 || free > total {
		return 0, 0, ErrAssets
	}
	return int64(total), int64(free), nil
}
