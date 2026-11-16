package files

import (
	"errors"

	"golang.org/x/sys/unix"
)

// renameat refuses to replace an existing target in the rename itself where
// the filesystem supports it, closing the race with writers outside this
// process too.
func renameat(fromFD int, from string, toFD int, to string) error {
	err := unix.Renameat2(fromFD, from, toFD, to, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.ENOTSUP) {
		return unix.Renameat(fromFD, from, toFD, to) // filesystem without RENAME_NOREPLACE
	}
	return err
}
