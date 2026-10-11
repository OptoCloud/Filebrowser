package files

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// FS is the confined view of files_root.
type FS struct {
	root *os.Root
	dir  string
}

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
	for _, d := range []string{TrashDir, UploadsDir} {
		if err := root.MkdirAll(d, 0o750); err != nil {
			_ = root.Close()
			return nil, fmt.Errorf("create %s: %w", d, err)
		}
	}
	return &FS{root: root, dir: abs}, nil
}

func (f *FS) Close() error { return f.root.Close() }

// Root exposes the os.Root for packages that need raw confined access.
func (f *FS) Root() *os.Root { return f.root }

// AbsUploadsDir is the on-disk directory for in-progress tus uploads. It lives
// inside files_root so completed uploads can be moved into place by rename.
func (f *FS) AbsUploadsDir() string { return filepath.Join(f.dir, filepath.FromSlash(UploadsDir)) }

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
	var pe *os.PathError
	if errors.As(err, &pe) && strings.Contains(pe.Err.Error(), "escapes") {
		return ErrForbidden
	}
	var le *os.LinkError
	if errors.As(err, &le) && strings.Contains(le.Err.Error(), "escapes") {
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
		if real == "" && name == Reserved {
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
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
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

// Rename moves from -> to, refusing to overwrite.
func (f *FS) Rename(from, to string) error {
	if _, err := f.root.Lstat(osPath(to)); err == nil {
		return ErrExists
	} else if !errors.Is(err, fs.ErrNotExist) {
		return mapErr(err)
	}
	return mapErr(f.root.Rename(osPath(from), osPath(to)))
}

func (f *FS) RemoveAll(p string) error {
	if p == "" || p == Reserved {
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
	for i := 0; i < 10000; i++ {
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

// Size returns the total size of p (recursively for directories).
func (f *FS) Size(p string) (int64, error) {
	var total int64
	err := fs.WalkDir(f.root.FS(), osPath(p), func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
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
