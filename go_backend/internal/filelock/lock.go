// Package filelock 通过操作系统锁限定单活动写入者，保留锁文件避免 inode 替换竞态。
package filelock

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
)

var ErrLocked = errors.New("存储目录已占用或无法锁定")

// Acquire 持有操作系统锁直至释放或进程退出；调用者负责创建目录。
func Acquire(path string) (func() error, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, ErrLocked
	}
	defer root.Close()
	name := filepath.Base(path)
	if info, e := root.Lstat(name); e == nil {
		if !info.Mode().IsRegular() {
			return nil, ErrLocked
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return nil, ErrLocked
	}
	f, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, ErrLocked
	}
	if err = lock(f); err != nil {
		f.Close()
		return nil, ErrLocked
	}
	var once sync.Once
	var closeErr error
	return func() error {
		once.Do(func() {
			unlockErr := unlock(f)
			err := f.Close()
			if unlockErr != nil || err != nil {
				closeErr = ErrLocked
			}
		})
		return closeErr
	}, nil
}
