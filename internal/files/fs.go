package files

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// FS is the confined view of files_root.
type FS struct {
	root *os.Root
	dir  string
	// rename is root.Rename; tests replace it to simulate crossing filesystems.
	rename func(from, to string) error
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
	return &FS{root: root, dir: abs, rename: root.Rename}, nil
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

func (f *FS) Stat(p string) (fs.FileInfo, error) {
	fi, err := f.root.Stat(osPath(p))
	return fi, mapErr(err)
}

func (f *FS) Lstat(p string) (fs.FileInfo, error) {
	fi, err := f.root.Lstat(osPath(p))
	return fi, mapErr(err)
}

func (f *FS) Open(p string) (*os.File, error) {
	file, err := f.root.Open(osPath(p))
	return file, mapErr(err)
}

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

func entryFromInfo(dir string, fi fs.FileInfo) Entry {
	e := Entry{Name: fi.Name(), Path: Join(dir, fi.Name()), IsDir: fi.IsDir(), ModTime: fi.ModTime().UTC()}
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
	d, err := f.Open(real)
	if err != nil {
		return nil, perm, err
	}
	defer d.Close()
	des, err := d.ReadDir(-1)
	if err != nil {
		if fi, serr := d.Stat(); serr == nil && !fi.IsDir() {
			return nil, perm, ErrNotDir
		}
		return nil, perm, mapErr(err)
	}
	var allowed map[string]bool
	if perm == PermTraverse {
		allowed = a.traverseChildren(real)
	}
	out := make([]Entry, 0, len(des))
	for _, de := range des {
		name := de.Name()
		if IsReservedName(name) {
			continue
		}
		if allowed != nil && !allowed[name] {
			continue
		}
		child := Join(real, name)
		if de.Type()&fs.ModeSymlink != 0 {
			target, err := f.Resolve(child)
			if err != nil || a.Perm(target) < PermRead {
				continue
			}
		}
		// Stat through the root so symlinks resolve (and escaping ones are hidden).
		fi, err := f.root.Stat(osPath(child))
		if err != nil {
			continue
		}
		if !fi.IsDir() && !fi.Mode().IsRegular() {
			continue
		}
		if allowed != nil && !fi.IsDir() {
			continue
		}
		out = append(out, entryFromInfo(dir, fi))
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
	return mapErr(f.root.Mkdir(osPath(p), 0o755))
}

func (f *FS) MkdirAll(p string) error {
	if p == "" {
		return nil
	}
	return mapErr(f.root.MkdirAll(osPath(p), 0o755))
}

func (f *FS) RemoveAll(p string) error {
	if p == "" || IsReservedName(p) {
		return ErrForbidden
	}
	return mapErr(f.root.RemoveAll(osPath(p)))
}

// UniquePath returns p, or "name (n).ext" in the same directory if p exists.
func (f *FS) UniquePath(p string) (string, error) {
	dir, name := Parent(p), Base(p)
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if ext == name {
		stem, ext = name, ""
	}
	for i := range 10000 {
		cand := p
		if i > 0 {
			cand = Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
		}
		_, err := f.root.Lstat(osPath(cand))
		if errors.Is(err, fs.ErrNotExist) {
			return cand, nil
		} else if err != nil {
			return "", mapErr(err)
		}
	}
	return "", ErrExists
}

// Size returns the total size of p (recursively for directories), not
// counting Reserved directories (trash and staged uploads).
func (f *FS) Size(p string) (int64, error) {
	var total int64
	err := fs.WalkDir(f.root.FS(), osPath(p), func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && IsReservedName(d.Name()) {
			return fs.SkipDir
		}
		if d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				total += fi.Size()
			}
		}
		return nil
	})
	return total, mapErr(err)
}
