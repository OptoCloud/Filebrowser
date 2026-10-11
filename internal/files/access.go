package files

import (
	"strings"

	"filebrowser/internal/store"
)

type Perm int

const (
	PermNone Perm = iota
	// PermTraverse: an ancestor of a grant. Only the path towards grants is listed.
	PermTraverse
	PermRead
	PermWrite
)

func (p Perm) String() string {
	return [...]string{"none", "traverse", "read", "write"}[p]
}

// Access describes what one user may touch. Admins have write access to everything.
type Access struct {
	Admin  bool
	Grants []store.Grant
}

func (a *Access) Perm(p string) Perm {
	if a.Admin {
		return PermWrite
	}
	best := PermNone
	for _, g := range a.Grants {
		switch {
		case Within(p, g.Path):
			if g.Permission == "write" {
				return PermWrite
			}
			best = max(best, PermRead)
		case Within(g.Path, p):
			best = max(best, PermTraverse)
		}
	}
	return best
}

// IsGrantRoot reports whether p is the root of one of the user's grants (or the
// storage root). Grant roots cannot be renamed, moved or deleted by the user.
func (a *Access) IsGrantRoot(p string) bool {
	if p == "" {
		return true
	}
	if a.Admin {
		return false
	}
	for _, g := range a.Grants {
		if g.Path == p {
			return true
		}
	}
	return false
}

// CanWrite reports whether the user holds any write grant (and therefore
// must use TOTP).
func (a *Access) CanWrite() bool {
	if a.Admin {
		return true
	}
	for _, g := range a.Grants {
		if g.Permission == "write" {
			return true
		}
	}
	return false
}

// traverseChildren returns the names of direct children of dir that lead to
// a grant, for a dir with PermTraverse.
func (a *Access) traverseChildren(dir string) map[string]bool {
	out := map[string]bool{}
	for _, g := range a.Grants {
		if g.Path == dir || !Within(g.Path, dir) {
			continue
		}
		rest := g.Path
		if dir != "" {
			rest = g.Path[len(dir)+1:]
		}
		name, _, _ := strings.Cut(rest, "/")
		out[name] = true
	}
	return out
}
