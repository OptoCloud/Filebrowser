// Package files implements confined file access over an os.Root.
//
// Paths in this package are slash-separated and relative to files_root, with ""
// meaning the root itself. They are produced only by Clean, which rejects
// traversal; os.Root additionally guarantees no escape via symlinks.
package files

import (
	"errors"
	"path"
	"runtime"
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

// IsReservedName reports whether a path component names Reserved, including
// the spellings that case-insensitive filesystems and Windows resolve to the
// same entry (".FILEBROWSER", ".filebrowser.", ".filebrowser "). Windows 8.3
// short names are caught by identity in Resolve.
func IsReservedName(c string) bool {
	return strings.EqualFold(strings.TrimRight(c, ". "), Reserved)
}

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
		if IsReservedName(c) {
			return "", ErrForbidden
		}
		if len(c) > 255 || (runtime.GOOS == "windows" && !portable(c)) {
			return "", ErrBadPath
		}
		out = append(out, c)
	}
	return strings.Join(out, "/"), nil
}

// ValidComponent checks a single path component that names an existing
// entry. On Windows it also refuses what the filesystem would read as
// something other than the name given: a colon (an alternate data stream, or
// "::$INDEX_ALLOCATION" for the directory itself) and trailing dots and
// spaces, which are dropped.
func ValidComponent(name string) error {
	if name == "" || name == "." || name == ".." || IsReservedName(name) || len(name) > 255 || !utf8.ValidString(name) ||
		strings.ContainsAny(name, "/\\\x00") || strings.TrimSpace(name) == "" {
		return ErrBadPath
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return ErrBadPath
		}
	}
	if runtime.GOOS == "windows" && !portable(name) {
		return ErrBadPath
	}
	return nil
}

// ValidName checks a name a client gives to something new (a folder, a
// rename, an upload). Besides ValidComponent it holds on every platform to
// the names all of them can store, so that what is made here can be copied
// anywhere: none of < > : " | ? *, no trailing dot or space, and none of
// Windows' device names. Files that already have such names stay readable.
func ValidName(name string) error {
	if err := ValidComponent(name); err != nil {
		return err
	}
	if !portable(name) {
		return ErrBadPath
	}
	return nil
}

// portable reports a name that means the same on every filesystem.
func portable(name string) bool {
	if strings.ContainsAny(name, `<>:"|?*`) || strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return false
	}
	// CON, NUL, COM1 and the like are devices on Windows, with any extension.
	stem, _, _ := strings.Cut(name, ".")
	switch stem = strings.ToUpper(strings.TrimRight(stem, " ")); stem {
	case "CON", "PRN", "AUX", "NUL":
		return false
	}
	if len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9' {
		return false
	}
	return true
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
