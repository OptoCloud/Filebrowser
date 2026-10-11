// Package files implements confined file access over an os.Root.
//
// Paths in this package are slash-separated and relative to files_root, with ""
// meaning the root itself. They are produced only by Clean, which rejects
// traversal; os.Root additionally guarantees no escape via symlinks.
package files

import (
	"errors"
	"path"
	"strings"
	"unicode/utf8"
)

var (
	ErrBadPath   = errors.New("invalid path")
	ErrForbidden = errors.New("forbidden")
	ErrNotFound  = errors.New("not found")
	ErrExists    = errors.New("already exists")
	ErrIsDir     = errors.New("is a directory")
	ErrNotDir    = errors.New("not a directory")
)

// Reserved names the directories holding trash, in-progress uploads and
// temporary copies: one below files_root, plus one at the top of each other
// filesystem mounted below it. Entries with this name, at any depth, are never
// visible or accessible through the API.
const Reserved = ".filebrowser"

const maxPathLen = 4096

// Clean validates a client-supplied path and returns its canonical form.
// It rejects rather than normalizes "..", so traversal attempts fail loudly.
func Clean(p string) (string, error) {
	if len(p) > maxPathLen || !utf8.ValidString(p) || strings.ContainsAny(p, "\x00\\") {
		return "", ErrBadPath
	}
	var out []string
	for c := range strings.SplitSeq(p, "/") {
		switch c {
		case "", ".":
			continue
		case "..":
			return "", ErrBadPath
		}
		if c == Reserved {
			return "", ErrForbidden
		}
		if len(c) > 255 {
			return "", ErrBadPath
		}
		out = append(out, c)
	}
	return strings.Join(out, "/"), nil
}

// ValidName checks a single path component supplied by a client (new file or
// folder names).
func ValidName(name string) error {
	if name == "" || name == "." || name == ".." || name == Reserved || len(name) > 255 || !utf8.ValidString(name) ||
		strings.ContainsAny(name, "/\\\x00") || strings.TrimSpace(name) == "" {
		return ErrBadPath
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return ErrBadPath
		}
	}
	return nil
}

// Within reports whether p equals base or lies below it.
func Within(p, base string) bool {
	return base == "" || p == base || strings.HasPrefix(p, base+"/")
}

func Join(dir, name string) string {
	if name == "" {
		return dir
	}
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

func Parent(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		return ""
	}
	return p[:i]
}

func Base(p string) string {
	if p == "" {
		return ""
	}
	return path.Base(p)
}

// osPath converts a cleaned path to the form os.Root expects.
func osPath(p string) string {
	if p == "" {
		return "."
	}
	return p
}
