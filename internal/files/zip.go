package files

import (
	"archive/zip"
	"context"
	"io"
	"io/fs"
	"strings"
	"time"
)

// WriteZip streams a zip of paths to w. Entries are named relative to the
// parent of each path. Callers must authorize every path first (and pass real
// paths); allow is consulted for the real target of every symlinked file found
// while walking. Media is usually already compressed, so entries are stored,
// not deflated.
func (f *FS) WriteZip(ctx context.Context, w io.Writer, paths []string, allow func(real string) bool) error {
	zw := zip.NewWriter(w)
	fsys := f.root.FS()
	seen := map[string]bool{}
	for _, p := range paths {
		prefix := Parent(p)
		err := fs.WalkDir(fsys, osPath(p), func(name string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // skip unreadable entries
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if d.Name() == Reserved {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			rel := name
			if prefix != "" {
				rel = strings.TrimPrefix(name, prefix+"/")
			}
			if name == "." {
				rel = "files"
			}
			if seen[rel] {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			seen[rel] = true
			if d.Type()&fs.ModeSymlink != 0 {
				// Follow file links only to targets the caller may read.
				target, err := f.Resolve(name)
				if err != nil || !allow(target) {
					return nil
				}
			}
			// Resolve symlinks through the root; escaping links fail and are skipped.
			fi, err := fs.Stat(fsys, name)
			if err != nil {
				return nil
			}
			if fi.IsDir() {
				if d.Type()&fs.ModeSymlink != 0 {
					return nil // do not follow directory symlinks (loops)
				}
				_, err := zw.CreateHeader(&zip.FileHeader{Name: rel + "/", Modified: fi.ModTime(), Method: zip.Store})
				return err
			}
			if !fi.Mode().IsRegular() {
				return nil
			}
			src, err := fsys.Open(name)
			if err != nil {
				return nil
			}
			defer src.Close()
			hdr := &zip.FileHeader{Name: rel, Modified: fi.ModTime(), Method: zip.Store}
			hdr.UncompressedSize64 = uint64(fi.Size()) // #nosec G115 -- regular file sizes are non-negative
			dst, err := zw.CreateHeader(hdr)
			if err != nil {
				return err
			}
			_, err = io.Copy(dst, src)
			return err
		})
		if err != nil {
			return err
		}
	}
	return zw.Close()
}

// Search walks the readable tree under start and returns entries whose name
// contains q (case-insensitive). It stops after limit hits or the deadline.
func (f *FS) Search(ctx context.Context, a *Access, start, q string, limit int, deadline time.Duration) ([]Entry, bool) {
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	q = strings.ToLower(q)
	var out []Entry
	truncated := false
	fsys := f.root.FS()

	var walk func(dir string)
	walk = func(dir string) {
		if truncated {
			return
		}
		real, err := f.Resolve(dir)
		if err != nil {
			return
		}
		switch min(a.Perm(dir), a.Perm(real)) {
		case PermRead, PermWrite:
			dir = real
			_ = fs.WalkDir(fsys, osPath(dir), func(name string, d fs.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if ctx.Err() != nil || len(out) >= limit {
					truncated = true
					return fs.SkipAll
				}
				if d.Name() == Reserved {
					if d.IsDir() {
						return fs.SkipDir
					}
					return nil
				}
				if name != "." && name != osPath(dir) && strings.Contains(strings.ToLower(d.Name()), q) {
					if fi, err := d.Info(); err == nil && (fi.IsDir() || fi.Mode().IsRegular()) {
						out = append(out, entryFromInfo(Parent(name), fi))
					}
				}
				return nil
			})
		case PermTraverse:
			for name := range a.traverseChildren(real) {
				walk(Join(real, name))
			}
		}
	}
	walk(start)
	return out, truncated
}
