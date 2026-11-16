package files

import (
	"archive/zip"
	"context"
	"io"
	"io/fs"
	"os"
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
	seen := map[string]bool{}
	for _, p := range paths {
		prefix := Parent(p)
		err := f.walk(p, func(name, base string, fi fs.FileInfo, open func() (*os.File, error)) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if IsReservedName(base) {
				if fi.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			rel := name
			if prefix != "" {
				rel = strings.TrimPrefix(name, prefix+"/")
			}
			if name == "" {
				rel = "files"
			}
			// Unzip tools treat '\' as a separator; a name created on the
			// host could otherwise extract outside the target folder.
			rel = strings.ReplaceAll(rel, `\`, "_")
			if seen[rel] {
				if fi.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			seen[rel] = true
			if fi.IsDir() {
				_, err := zw.CreateHeader(&zip.FileHeader{Name: rel + "/", Modified: fi.ModTime(), Method: zip.Store})
				return err
			}
			var src *os.File
			switch {
			case fi.Mode()&fs.ModeSymlink != 0:
				// Follow file links only to targets the caller may read;
				// directory links are not followed (loops).
				target, err := f.Resolve(name)
				if err != nil || !allow(target) {
					return nil
				}
				if src, fi, err = f.OpenFile(target); err != nil {
					return nil
				}
			case fi.Mode().IsRegular():
				var err error
				if src, err = open(); err != nil {
					return nil // skip unreadable entries
				}
			default:
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
			_ = f.walk(real, func(name, base string, fi fs.FileInfo, _ func() (*os.File, error)) error {
				if ctx.Err() != nil || len(out) >= limit {
					truncated = true
					return fs.SkipAll
				}
				if IsReservedName(base) {
					if fi.IsDir() {
						return fs.SkipDir
					}
					return nil
				}
				if name != real && strings.Contains(strings.ToLower(base), q) && (fi.IsDir() || fi.Mode().IsRegular()) {
					out = append(out, entryFromInfo(Parent(name), base, fi))
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
