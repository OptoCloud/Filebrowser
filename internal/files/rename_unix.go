//go:build unix

package files

import (
	"os"
	"runtime"
)

// renameAt renames the entry at the real path from to the real path to. Both
// directories are reached without following symlinks and the rename is made
// between those two open directories, so neither end can be redirected into
// another tree while it happens.
func (f *FS) renameAt(from, to string) error {
	fromDir, fromName, err := f.parent(from)
	if err != nil {
		return err
	}
	defer fromDir.Close()
	toDir, toName, err := f.parent(to)
	if err != nil {
		return err
	}
	defer toDir.Close()
	fd, err := fromDir.Open(".")
	if err != nil {
		return err
	}
	defer fd.Close()
	td, err := toDir.Open(".")
	if err != nil {
		return err
	}
	defer td.Close()
	err = renameat(int(fd.Fd()), fromName, int(td.Fd()), toName) // #nosec G115 -- file descriptors fit in int
	runtime.KeepAlive(fd)
	runtime.KeepAlive(td)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	return nil
}
