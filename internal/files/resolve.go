package files

import (
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
)

// maxSymlinkHops bounds symlink expansion (loops, deep chains).
const maxSymlinkHops = 40

// Resolve expands symlinks in p and returns the real path relative to
// files_root. os.Root alone only guarantees that a path stays inside
// files_root; symlinks that stay inside it would otherwise let a path inside
// one grant reach files in another, so permissions must be checked on the
// result of Resolve. Components that do not exist yet are kept as given.
func (f *FS) Resolve(p string) (string, error) {
	parts := splitPath(p)
	var out []string
	hops := 0
	for i := 0; i < len(parts); i++ {
		cand := strings.Join(append(out[:len(out):len(out)], parts[i]), "/")
		fi, err := f.root.Lstat(cand)
		if errors.Is(err, fs.ErrNotExist) {
			out = append(out, parts[i:]...)
			break
		} else if err != nil {
			return "", mapErr(err)
		}
		if f.aliasesReserved(strings.Join(out, "/"), parts[i], fi) {
			return "", ErrForbidden
		}
		if fi.Mode()&fs.ModeSymlink == 0 {
			out = append(out, parts[i])
			continue
		}
		if hops++; hops > maxSymlinkHops {
			return "", ErrForbidden
		}
		target, err := f.root.Readlink(cand)
		if err != nil {
			return "", mapErr(err)
		}
		target = filepath.ToSlash(target)
		if strings.HasPrefix(target, "/") || filepath.IsAbs(target) || filepath.VolumeName(target) != "" {
			return "", ErrForbidden // absolute links are rejected by os.Root as well
		}
		// Continue resolving from the link's directory with the target spliced in.
		next := append(append(append([]string{}, out...), splitPath(target)...), parts[i+1:]...)
		parts, out, i = nil, nil, -1
		for _, c := range next {
			if c == ".." {
				if len(parts) == 0 {
					return "", ErrForbidden // escapes files_root
				}
				parts = parts[:len(parts)-1]
				continue
			}
			parts = append(parts, c)
		}
	}
	real := strings.Join(out, "/")
	if hasReserved(real) {
		return "", ErrForbidden
	}
	return real, nil
}

func splitPath(p string) []string {
	var out []string
	for c := range strings.SplitSeq(p, "/") {
		if c != "" && c != "." {
			out = append(out, c)
		}
	}
	return out
}

// Authorize checks that a has want on p and on the real target of p (after
// symlink resolution) and returns the real path, which callers must use for
// the actual file operation.
func (f *FS) Authorize(a *Access, p string, want Perm) (string, error) {
	if err := Need(a, p, want); err != nil {
		return "", err
	}
	real, err := f.Resolve(p)
	if err != nil {
		return "", err
	}
	if err := Need(a, real, want); err != nil {
		return "", err
	}
	return real, nil
}

// AuthorizeEntry is like Authorize but does not follow a symlink in the final
// component, for operations on the directory entry itself (rename, move,
// delete).
func (f *FS) AuthorizeEntry(a *Access, p string, want Perm) (string, error) {
	if p == "" {
		return f.Authorize(a, p, want)
	}
	if err := Need(a, p, want); err != nil {
		return "", err
	}
	dir, err := f.Resolve(Parent(p))
	if err != nil {
		return "", err
	}
	real := Join(dir, Base(p))
	if fi, err := f.root.Lstat(osPath(real)); err == nil && f.aliasesReserved(dir, Base(p), fi) {
		return "", ErrForbidden
	}
	if err := Need(a, real, want); err != nil {
		return "", err
	}
	return real, nil
}
