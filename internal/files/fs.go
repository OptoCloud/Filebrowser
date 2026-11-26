package files

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// FS is the confined view of files_root.
type FS struct {
	root *os.Root
	dir  string
	// rename is root.Rename; tests replace it to simulate crossing filesystems.
	rename func(from, to string) error
	// renameMu makes "target absent, then rename" one step for this process
	// (see renameNoReplace).
	renameMu sync.Mutex
}

// Central reserved directories. Trash and staged uploads may also live in a
// Reserved directory on another filesystem below files_root (see ReservedDir).
const (
	TrashDir   = Reserved + "/trash"
	UploadsDir = Reserved + "/uploads"
)

func OpenFS(dir string) (*FS, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, err
	}
	for _, d := range []string{TrashDir, UploadsDir, Reserved + "/tmp"} {
		if err := root.MkdirAll(d, 0o750); err != nil {
			_ = root.Close()
			return nil, fmt.Errorf("create %s: %w", d, err)
		}
	}
	fsys := &FS{root: root, dir: abs}
	fsys.rename = fsys.renameAt
	return fsys, nil
}

func (f *FS) Close() error { return f.root.Close() }

// Root exposes the os.Root for packages that need raw confined access.
func (f *FS) Root() *os.Root { return f.root }

func mapErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return ErrNotFound
	case errors.Is(err, fs.ErrExist):
		return ErrExists
	case errors.Is(err, fs.ErrPermission):
		return ErrForbidden
	}
	if pe, ok := errors.AsType[*os.PathError](err); ok && strings.Contains(pe.Err.Error(), "escapes") {
		return ErrForbidden
	}
	if le, ok := errors.AsType[*os.LinkError](err); ok && strings.Contains(le.Err.Error(), "escapes") {
		return ErrForbidden
	}
	return err
}

// The methods below take real paths (see Resolve) and never follow a symlink
// in them (see nofollow.go).

// Stat describes the file or directory at the real path p. A real path holds
// no symlink, so finding one there means it was swapped in: it is refused.
func (f *FS) Stat(p string) (fs.FileInfo, error) {
	fi, err := f.lstat(p)
	if err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		return nil, ErrForbidden
	}
	return fi, err
}

// Lstat describes the entry at p itself, which may be a symlink.
func (f *FS) Lstat(p string) (fs.FileInfo, error) { return f.lstat(p) }

func (f *FS) Open(p string) (*os.File, error) { return f.open(p) }

// OpenFile opens a regular file for reading and returns it with its info.
func (f *FS) OpenFile(p string) (*os.File, fs.FileInfo, error) {
	file, err := f.Open(p)
	if err != nil {
		return nil, nil, err
	}
	fi, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, mapErr(err)
	}
	if fi.IsDir() {
		_ = file.Close()
		return nil, nil, ErrIsDir
	}
	if !fi.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, ErrForbidden
	}
	return file, fi, nil
}

type Entry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	IsDir   bool      `json:"is_dir"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mtime"`
	Mime    string    `json:"mime,omitempty"`
}

func entryFromInfo(dir, name string, fi fs.FileInfo) Entry {
	e := Entry{Name: name, Path: Join(dir, name), IsDir: fi.IsDir(), ModTime: fi.ModTime().UTC()}
	if !e.IsDir {
		e.Size = fi.Size()
		e.Mime = MimeType(e.Name)
	}
	return e
}

// List returns the visible entries of dir for the given access. Entry paths
// are reported under dir as requested; symlinked entries are shown only when
// their real target is readable.
func (f *FS) List(a *Access, dir string) ([]Entry, Perm, error) {
	real, err := f.Authorize(a, dir, PermTraverse)
	if err != nil {
		return nil, PermNone, err
	}
	perm := min(a.Perm(dir), a.Perm(real))
	d, err := f.openDir(real)
	if err != nil {
		return nil, perm, err
	}
	defer d.Close()
	names, err := readNames(d)
	if err != nil {
		return nil, perm, mapErr(err)
	}
	var allowed map[string]bool
	if perm == PermTraverse {
		allowed = a.traverseChildren(real)
	}
	out := make([]Entry, 0, len(names))
	for _, name := range names {
		if IsReservedName(name) {
			continue
		}
		if allowed != nil && !allowed[name] {
			continue
		}
		fi, err := d.Lstat(name)
		if err != nil {
			continue
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			// Shown as its target, if that is readable (escaping links are hidden).
			target, err := f.Resolve(Join(real, name))
			if err != nil || a.Perm(target) < PermRead {
				continue
			}
			if fi, err = f.Stat(target); err != nil {
				continue
			}
		}
		if !fi.IsDir() && !fi.Mode().IsRegular() {
			continue
		}
		if allowed != nil && !fi.IsDir() {
			continue
		}
		e := entryFromInfo(dir, name, fi)
		// A name without an extension says nothing: such files are typed by
		// their content, so they get the right icon and viewer.
		if !e.IsDir && path.Ext(name) == "" && fi.Mode().IsRegular() && fi.Mode()&fs.ModeSymlink == 0 {
			if file, err := openIn(d, name, fi); err == nil {
				if t, err := VerifyFile(name, file, fi.Size()); err == nil {
					e.Mime = t.Mime
				}
				_ = file.Close()
			}
		}
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b Entry) int {
		if a.IsDir != b.IsDir {
			if a.IsDir {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return out, perm, nil
}

func (f *FS) Mkdir(p string) error {
	return f.in(p, func(dir *os.Root, name string) error { return dir.Mkdir(name, 0o755) })
}

func (f *FS) MkdirAll(p string) error { return f.mkdirAll(p, 0o755) }

func (f *FS) mkdirAll(p string, perm fs.FileMode) error {
	if p == "" {
		return nil
	}
	dir, err := f.openDirPerm(p, perm)
	if err != nil {
		return err
	}
	return dir.Close()
}

func (f *FS) RemoveAll(p string) error {
	if p == "" || IsReservedName(p) {
		return ErrForbidden
	}
	return f.removeAll(p)
}

// removeAll removes the entry at p with everything below it; a symlink is
// removed, not followed.
func (f *FS) removeAll(p string) error {
	return f.in(p, func(dir *os.Root, name string) error { return dir.RemoveAll(name) })
}

// UniquePath returns p, or "name (n).ext" in the same directory if p exists.
func (f *FS) UniquePath(p string) (string, error) {
	parent, name, err := f.parent(p)
	if err != nil {
		return "", err
	}
	defer parent.Close()
	dir := Parent(p)
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if ext == name {
		stem, ext = name, ""
	}
	for i := range 10000 {
		cand := name
		if i > 0 {
			cand = fmt.Sprintf("%s (%d)%s", stem, i, ext)
		}
		_, err := parent.Lstat(cand)
		if errors.Is(err, fs.ErrNotExist) {
			return Join(dir, cand), nil
		} else if err != nil {
			return "", mapErr(err)
		}
	}
	return "", ErrExists
}

// Size returns the total size of p (recursively for directories), not
// counting Reserved directories (trash and staged uploads).
// SizeContext is Size that gives up when ctx is done, reporting whether the
// whole tree was counted.
func (f *FS) SizeContext(ctx context.Context, p string) (total int64, whole bool) {
	whole = true
	_ = f.walk(p, func(_, name string, fi fs.FileInfo, _ func() (*os.File, error)) error {
		if ctx.Err() != nil {
			whole = false
			return fs.SkipAll
		}
		if fi.IsDir() && IsReservedName(name) {
			return fs.SkipDir
		}
		if fi.Mode().IsRegular() {
			total += fi.Size()
		}
		return nil
	})
	return total, whole
}

func (f *FS) Size(p string) (int64, error) {
	var total int64
	err := f.walk(p, func(_, name string, fi fs.FileInfo, _ func() (*os.File, error)) error {
		if fi.IsDir() && IsReservedName(name) {
			return fs.SkipDir
		}
		if fi.Mode().IsRegular() {
			total += fi.Size()
		}
		return nil
	})
	return total, err
}
