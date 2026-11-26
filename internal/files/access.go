package files

import (
	"context"
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

// AccessFor loads the current access of u. It is re-read on every request,
// so grant changes take effect immediately.
func AccessFor(ctx context.Context, st *store.Store, u *store.User) (*Access, error) {
	grants, err := st.Grants(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	return &Access{Admin: u.IsAdmin(), Grants: grants}, nil
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

// Narrow returns the part of a that lies within paths (” being all of it),
// read-only unless write is set: the access of an API token, which is never
// more than its owner's and follows the owner's grants as they change.
func (a *Access) Narrow(paths []string, write bool) *Access {
	perm := func(owner string) string {
		if write && owner == "write" {
			return "write"
		}
		return "read"
	}
	out := &Access{}
	add := func(p, permission string) {
		for i, g := range out.Grants {
			if g.Path == p {
				if permission == "write" {
					out.Grants[i].Permission = "write"
				}
				return
			}
		}
		out.Grants = append(out.Grants, store.Grant{Path: p, Permission: permission})
	}
	for _, p := range paths {
		if a.Admin {
			add(p, perm("write"))
			continue
		}
		for _, g := range a.Grants {
			switch {
			case Within(p, g.Path): // the token's path is inside the grant
				add(p, perm(g.Permission))
			case Within(g.Path, p): // the grant is inside the token's path
				add(g.Path, perm(g.Permission))
			}
		}
	}
	return out
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
