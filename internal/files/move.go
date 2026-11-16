package files

import (
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"strings"

	"filebrowser/internal/auth"
)

// files_root may span several filesystems (for example one mount per share
// below it). A rename cannot cross them, so:
//   - trash and in-progress uploads live in a Reserved directory at the top of
//     the filesystem that holds the item (ReservedDir), keeping those moves
//     renames;
//   - Move falls back to copy-then-delete for moves that do cross filesystems
//     (between shares, or where the layout cannot be detected).

// ReservedDir returns Reserved/sub on the filesystem holding dir (a real
// path), creating it. It lies at the topmost directory of that filesystem
// within files_root, which is files_root itself when there is only one. The
// bool reports whether the result is known to share dir's filesystem; if the
// layout cannot be detected (no device ids on this platform) or the directory
// cannot be created there, the central one below files_root is returned.
func (f *FS) ReservedDir(dir, sub string) (string, bool) {
	top, known := f.filesystemTop(dir)
	if top != "" {
		p := Join(top, Reserved+"/"+sub)
		if err := f.mkdirAll(p, 0o750); err == nil {
			return p, true
		}
		known = false // e.g. a read-only mount: use the central directory
	}
	p := Reserved + "/" + sub
	if err := f.mkdirAll(p, 0o750); err != nil {
		slog.Error("create reserved directory", "path", p, "err", err)
	}
	return p, known
}

// filesystemTop returns the topmost ancestor of dir (or dir itself) on the
// same filesystem as dir, "" for files_root. A dir that does not exist yet
// belongs to the filesystem of its nearest existing ancestor. ok is false if
// device ids are unavailable.
func (f *FS) filesystemTop(dir string) (top string, ok bool) {
	fi, err := f.Stat(dir)
	for errors.Is(err, ErrNotFound) && dir != "" {
		dir = Parent(dir)
		fi, err = f.Stat(dir)
	}
	if err != nil {
		return "", false
	}
	dev, ok := deviceID(fi)
	if !ok {
		return "", false
	}
	for top = dir; top != ""; top = Parent(top) {
		pfi, err := f.Stat(Parent(top))
		if err != nil {
			return "", false
		}
		if pdev, _ := deviceID(pfi); pdev != dev {
			return top, true
		}
	}
	return "", true
}

// Move moves the entry from to the path to (both real paths), refusing to
// overwrite. Across filesystems the tree is copied into a hidden temporary
// location on the destination filesystem, renamed into place, and only then
// is the source removed, so a partial copy never appears under its final name.
func (f *FS) Move(from, to string) error {
	err := f.renameNoReplace(from, to)
	if err == nil || !isCrossDevice(err) {
		return mapErr(err)
	}
	tmpDir, _ := f.ReservedDir(Parent(to), "tmp")
	tmp := Join(tmpDir, newID())
	if err := f.copyTree(from, tmp); err != nil {
		return mapErr(err)
	}
	if err := f.renameNoReplace(tmp, to); err != nil {
		_ = f.removeAll(tmp)
		if !isCrossDevice(err) {
			return mapErr(err)
		}
		// Filesystem layout unknown: copy straight to the destination.
		if err := f.copyTree(from, to); err != nil {
			return mapErr(err)
		}
	}
	if err := f.removeAll(from); err != nil {
		// The data is safe at its destination; only the source lingers.
		slog.Warn("move: copied but could not remove source", "from", from, "err", err)
	}
	return nil
}

// renameNoReplace renames from to to (real paths) unless to exists. Not every
// platform and filesystem can refuse to replace in the rename itself (see
// renameAt), so the check and the rename also happen under one lock: two of
// our own operations (say, uploads of the same name finishing together) can
// never overwrite each other. Where the rename cannot refuse, a writer
// outside this process (SMB, a shell) can still race it.
func (f *FS) renameNoReplace(from, to string) error {
	f.renameMu.Lock()
	defer f.renameMu.Unlock()
	if err := f.absent(to); err != nil {
		return err
	}
	return f.rename(from, to)
}

// FileID identifies the file or folder at the real path p independently of
// its name, so callers can tell when something else has replaced it. It is ""
// where the platform offers no such identity.
func (f *FS) FileID(p string) (string, error) { return f.fileID(p) }

// absent returns ErrExists if p exists (as any kind of entry, even a dangling symlink).
func (f *FS) absent(p string) error {
	if _, err := f.lstat(p); err == nil {
		return ErrExists
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	return nil
}

// copyTree copies from to to (which must not exist), preserving modes and
// modification times; symlinks are copied as links. Special files are
// skipped. On failure it removes what it created.
func (f *FS) copyTree(from, to string) (err error) {
	fi, err := f.lstat(from)
	if err != nil {
		return err
	}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		var target string
		err := f.in(from, func(dir *os.Root, name string) (err error) {
			target, err = dir.Readlink(name)
			return err
		})
		if err != nil {
			return err
		}
		return f.in(to, func(dir *os.Root, name string) error { return dir.Symlink(target, name) })
	case fi.IsDir():
		if err := f.in(to, func(dir *os.Root, name string) error { return dir.Mkdir(name, 0o700) }); err != nil {
			return err
		}
		defer func() {
			if err != nil {
				_ = f.removeAll(to)
			}
		}()
		src, err := f.openDir(from)
		if err != nil {
			return err
		}
		names, err := readNames(src)
		_ = src.Close()
		if err != nil {
			return err
		}
		for _, name := range names {
			if IsReservedName(name) {
				continue // trash and staged uploads of a nested filesystem stay where they are
			}
			if err := f.copyTree(Join(from, name), Join(to, name)); err != nil {
				return err
			}
		}
		return f.touch(to, fi.Mode().Perm(), fi.ModTime())
	case fi.Mode().IsRegular():
		return f.copyFile(from, to, fi)
	}
	return nil
}

func (f *FS) copyFile(from, to string, fi fs.FileInfo) (err error) {
	src, err := f.open(from)
	if err != nil {
		return err
	}
	defer src.Close()
	var dst *os.File
	// O_EXCL never follows a symlink at the new name.
	err = f.in(to, func(dir *os.Root, name string) (err error) {
		dst, err = dir.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fi.Mode().Perm())
		return err
	})
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = f.in(to, func(dir *os.Root, name string) error { return dir.Remove(name) })
		}
	}()
	if _, err = io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return err
	}
	if err = dst.Close(); err != nil {
		return err
	}
	return f.touch(to, fi.Mode().Perm(), fi.ModTime())
}

// newID returns a random identifier for trash entries and temporary copies.
func newID() string { return auth.NewToken(12) }

// hasReserved reports whether any component of the cleaned path p is Reserved.
func hasReserved(p string) bool {
	for c := range strings.SplitSeq(p, "/") {
		if IsReservedName(c) {
			return true
		}
	}
	return false
}

// aliasesReserved reports whether fi, the entry name in dir, is dir's
// Reserved directory under another name (a Windows 8.3 short name such as
// FILEBR~1).
func (f *FS) aliasesReserved(dir, name string, fi fs.FileInfo) bool {
	if !strings.Contains(name, "~") || !fi.IsDir() {
		return false
	}
	rfi, err := f.root.Lstat(osPath(Join(dir, Reserved)))
	return err == nil && os.SameFile(fi, rfi)
}
