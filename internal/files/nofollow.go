package files

import (
	"errors"
	"io/fs"
	"os"
	"slices"
	"strings"
	"time"
)

// Permissions are checked on the real path that Resolve returns, but a path
// is only a name: between that check and the file operation, someone with
// write access to a directory on the way can rename an existing symlink over
// one of its components, and os.Root would follow it into a tree the caller
// has no grant for. So no operation on a real path trusts the path. Each one
// walks it one component at a time (openDir), refusing symlinks, and then
// acts on a name inside the directory handle it reached: what was checked is
// what is used.

// step opens the directory name inside dir, refusing a symlink. Lstat and
// open are two steps, so the opened directory is compared with what Lstat
// saw: a link swapped in between leads somewhere else and is refused.
func step(dir *os.Root, name string) (*os.Root, error) {
	lfi, err := dir.Lstat(name)
	if err != nil {
		return nil, err
	}
	if lfi.Mode()&fs.ModeSymlink != 0 {
		return nil, ErrForbidden
	}
	if !lfi.IsDir() {
		return nil, ErrNotDir
	}
	sub, err := dir.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	fi, err := sub.Stat(".")
	if err != nil || !os.SameFile(lfi, fi) {
		_ = sub.Close()
		if err == nil {
			err = ErrForbidden
		}
		return nil, err
	}
	return sub, nil
}

// openDir opens the directory at the real path p without following a symlink
// in any component. With mkPerm != 0, missing directories are created. The
// caller closes the result.
func (f *FS) openDirPerm(p string, mkPerm fs.FileMode) (*os.Root, error) {
	cur, err := f.root.OpenRoot(".")
	if err != nil {
		return nil, mapErr(err)
	}
	for _, c := range splitPath(p) {
		next, err := step(cur, c)
		if mkPerm != 0 && errors.Is(err, fs.ErrNotExist) {
			if err = cur.Mkdir(c, mkPerm); err == nil || errors.Is(err, fs.ErrExist) {
				next, err = step(cur, c)
			}
		}
		_ = cur.Close()
		if err != nil {
			return nil, mapErr(err)
		}
		cur = next
	}
	return cur, nil
}

func (f *FS) openDir(p string) (*os.Root, error) { return f.openDirPerm(p, 0) }

// parent opens the directory holding the real path p (which must not be the
// root) and returns it with p's name in it.
func (f *FS) parent(p string) (*os.Root, string, error) {
	if p == "" {
		return nil, "", ErrForbidden
	}
	dir, err := f.openDir(Parent(p))
	if err != nil {
		return nil, "", err
	}
	return dir, Base(p), nil
}

// in runs fn on the directory holding p and p's name in it.
func (f *FS) in(p string, fn func(dir *os.Root, name string) error) error {
	dir, name, err := f.parent(p)
	if err != nil {
		return err
	}
	defer dir.Close()
	return mapErr(fn(dir, name))
}

// lstat is Lstat for any real path, including the root.
func (f *FS) lstat(p string) (fs.FileInfo, error) {
	if p == "" {
		fi, err := f.root.Stat(".")
		return fi, mapErr(err)
	}
	var fi fs.FileInfo
	err := f.in(p, func(dir *os.Root, name string) (err error) {
		fi, err = dir.Lstat(name)
		return err
	})
	return fi, err
}

// openIn opens name in dir, which lfi (from dir.Lstat) describes as a regular
// file or directory, and checks that this is what was opened.
func openIn(dir *os.Root, name string, lfi fs.FileInfo) (*os.File, error) {
	if !lfi.IsDir() && !lfi.Mode().IsRegular() {
		return nil, ErrForbidden // symlinks, and devices or pipes that could block
	}
	file, err := dir.Open(name)
	if err != nil {
		return nil, err
	}
	fi, err := file.Stat()
	if err != nil || !os.SameFile(lfi, fi) {
		_ = file.Close()
		if err == nil {
			err = ErrForbidden
		}
		return nil, err
	}
	return file, nil
}

// open opens the regular file or directory at the real path p.
func (f *FS) open(p string) (*os.File, error) {
	if p == "" {
		file, err := f.root.Open(".")
		return file, mapErr(err)
	}
	var file *os.File
	err := f.in(p, func(dir *os.Root, name string) error {
		lfi, err := dir.Lstat(name)
		if err != nil {
			return err
		}
		file, err = openIn(dir, name, lfi)
		return err
	})
	return file, err
}

// touch sets mode and modification time on the entry at p, which must not be
// a symlink. (Chmod and Chtimes follow links; the check before them leaves
// only metadata of an in-root file exposed to a swap in between.)
func (f *FS) touch(p string, mode fs.FileMode, mtime time.Time) error {
	return f.in(p, func(dir *os.Root, name string) error {
		lfi, err := dir.Lstat(name)
		if err != nil {
			return err
		}
		if lfi.Mode()&fs.ModeSymlink != 0 {
			return ErrForbidden
		}
		if err := dir.Chmod(name, mode); err != nil {
			return err
		}
		return dir.Chtimes(name, mtime, mtime)
	})
}

// walkFunc is called for each entry of a walk with its real path, its name
// and its Lstat info (symlinks are reported, never followed). open opens the
// entry if it is a regular file. Returning fs.SkipDir for a directory skips
// its contents; fs.SkipAll ends the walk.
type walkFunc func(p, name string, fi fs.FileInfo, open func() (*os.File, error)) error

// walk visits the tree at the real path p in lexical order, holding each
// directory open while it is read so that nothing swapped in during the walk
// can redirect it. Entries that cannot be read are skipped.
func (f *FS) walk(p string, fn walkFunc) error {
	var err error
	if p == "" {
		var dir *os.Root
		var fi fs.FileInfo
		if dir, err = f.openDir(""); err != nil {
			return err
		}
		defer dir.Close()
		if fi, err = dir.Stat("."); err != nil {
			return mapErr(err)
		}
		if err = fn("", ".", fi, nil); err == nil {
			err = f.walkDir(dir, "", fn)
		}
	} else {
		var dir *os.Root
		var name string
		if dir, name, err = f.parent(p); err != nil {
			return err
		}
		defer dir.Close()
		err = f.walkEntry(dir, p, name, fn)
	}
	if err == fs.SkipAll || err == fs.SkipDir {
		return nil
	}
	return err
}

func (f *FS) walkEntry(dir *os.Root, p, name string, fn walkFunc) error {
	fi, err := dir.Lstat(name)
	if err != nil {
		return nil
	}
	err = fn(p, name, fi, func() (*os.File, error) { return openIn(dir, name, fi) })
	if err != nil || !fi.IsDir() {
		if err == fs.SkipDir && fi.IsDir() {
			return nil
		}
		return err
	}
	sub, err := step(dir, name)
	if err != nil {
		return nil
	}
	defer sub.Close()
	return f.walkDir(sub, p, fn)
}

func (f *FS) walkDir(dir *os.Root, p string, fn walkFunc) error {
	names, err := readNames(dir)
	if err != nil {
		return nil
	}
	for _, name := range names {
		if err := f.walkEntry(dir, Join(p, name), name, fn); err != nil {
			if err == fs.SkipDir {
				return nil // from a file: skip the rest of this directory
			}
			return err
		}
	}
	return nil
}

// readNames returns the sorted entry names of an open directory.
func readNames(dir *os.Root) ([]string, error) {
	d, err := dir.Open(".")
	if err != nil {
		return nil, err
	}
	defer d.Close()
	names, err := d.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(names, strings.Compare)
	return names, nil
}
