package images

import (
	"golang.org/x/sys/windows"
	"os"
)

func diskSpace(root *os.Root) (int64, int64, error) {
	p, e := windows.UTF16PtrFromString(root.Name())
	if e != nil {
		return 0, 0, ErrAssets
	}
	var available, total, free uint64
	if windows.GetDiskFreeSpaceEx(p, &available, &total, &free) != nil || total > 1<<63-1 || available > total {
		return 0, 0, ErrAssets
	}
	return int64(total), int64(available), nil
}
