//go:build unix

package files

import (
	"errors"
	"fmt"
	"io/fs"
	"syscall"
)

// deviceID returns the filesystem (device) id of fi.
func deviceID(fi fs.FileInfo) (uint64, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Dev), true // #nosec G115 -- Dev is a device number; the cast is lossless or a no-op
}

// fileID identifies the file at p (real path) by device and inode.
func (f *FS) fileID(p string) (string, error) {
	fi, err := f.root.Stat(osPath(p))
	if err != nil {
		return "", mapErr(err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return "", nil
	}
	return fmt.Sprintf("%x:%x", st.Dev, st.Ino), nil
}

// errCrossDevice is what a rename across filesystems fails with (for tests).
var errCrossDevice error = syscall.EXDEV

// isCrossDevice reports a rename that failed because it crossed filesystems.
func isCrossDevice(err error) bool { return errors.Is(err, syscall.EXDEV) }
