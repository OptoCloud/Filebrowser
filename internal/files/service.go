package files

import (
	"context"
	"errors"
	"sync"
	"time"

	"filebrowser/internal/auth"
	"filebrowser/internal/store"
)

// Service applies permission checks on top of FS. All paths given to it must
// already have been through Clean.
type Service struct {
	FS    *FS
	Store *store.Store

	usageMu  sync.Mutex
	usage    map[int64]usageEntry
	reserved map[string]reservation // in-flight uploads by upload ID
}

type reservation struct {
	userID int64
	bytes  int64
	at     time.Time
}

type usageEntry struct {
	bytes int64
	at    time.Time
}

func NewService(fsys *FS, st *store.Store) *Service {
	return &Service{FS: fsys, Store: st, usage: map[int64]usageEntry{}, reserved: map[string]reservation{}}
}

// Need checks that a has at least want on p. Paths with no access at all are
// reported as not found so their existence is not revealed.
func Need(a *Access, p string, want Perm) error {
	got := a.Perm(p)
	if got >= want {
		return nil
	}
	if got == PermNone {
		return ErrNotFound
	}
	return ErrForbidden
}

func (s *Service) Mkdir(a *Access, dir, name string) (string, error) {
	if err := ValidName(name); err != nil {
		return "", err
	}
	real, err := s.FS.Authorize(a, dir, PermWrite)
	if err != nil {
		return "", err
	}
	p := Join(real, name)
	return p, s.FS.Mkdir(p)
}

// entry authorizes an operation on the directory entry p itself (not the
// target of a symlink) and refuses grant roots.
func (s *Service) entry(a *Access, p string) (string, error) {
	if a.IsGrantRoot(p) {
		return "", ErrForbidden
	}
	real, err := s.FS.AuthorizeEntry(a, p, PermWrite)
	if err != nil {
		return "", err
	}
	if a.IsGrantRoot(real) {
		return "", ErrForbidden
	}
	return real, nil
}

func (s *Service) Rename(a *Access, p, newName string) (string, error) {
	if err := ValidName(newName); err != nil {
		return "", err
	}
	real, err := s.entry(a, p)
	if err != nil {
		return "", err
	}
	to := Join(Parent(real), newName)
	if to == real {
		return real, nil
	}
	return to, s.FS.Rename(real, to)
}

// Move moves each path into dest. It stops at the first error and returns the
// new paths of items moved so far.
func (s *Service) Move(a *Access, paths []string, dest string) ([]string, error) {
	dest, err := s.FS.Authorize(a, dest, PermWrite)
	if err != nil {
		return nil, err
	}
	if fi, err := s.FS.Stat(dest); err != nil {
		return nil, err
	} else if !fi.IsDir() {
		return nil, ErrNotDir
	}
	var moved []string
	for _, p := range paths {
		real, err := s.entry(a, p)
		if err != nil {
			return moved, err
		}
		if Within(dest, real) {
			return moved, ErrBadPath
		}
		to := Join(dest, Base(real))
		if to == real {
			continue
		}
		if err := s.FS.Rename(real, to); err != nil {
			return moved, err
		}
		moved = append(moved, to)
	}
	return moved, nil
}

// Delete moves paths to the user's trash.
func (s *Service) Delete(ctx context.Context, userID int64, a *Access, paths []string) ([]*store.TrashItem, error) {
	var items []*store.TrashItem
	for _, p := range paths {
		p, err := s.entry(a, p)
		if err != nil {
			return items, err
		}
		fi, err := s.FS.Lstat(p)
		if err != nil {
			return items, err
		}
		id := auth.NewToken(12)
		dir := TrashDir + "/" + id
		if err := s.FS.Root().Mkdir(dir, 0o750); err != nil {
			return items, mapErr(err)
		}
		size := fi.Size()
		if fi.IsDir() {
			size, _ = s.FS.Size(p)
		}
		tp := dir + "/" + Base(p)
		if err := s.FS.Rename(p, tp); err != nil {
			_ = s.FS.Root().Remove(dir)
			return items, err
		}
		item := &store.TrashItem{ID: id, UserID: userID, OriginalPath: p, TrashPath: tp, IsDir: fi.IsDir(), Size: size, TrashedAt: time.Now().UTC()}
		if err := s.Store.AddTrash(ctx, item); err != nil {
			// Put it back rather than orphaning it in the trash.
			_ = s.FS.Rename(tp, p)
			_ = s.FS.Root().Remove(dir)
			return items, err
		}
		items = append(items, item)
	}
	s.InvalidateUsage(userID)
	return items, nil
}

// Restore moves a trashed item back to its original location (renamed if the
// name is taken). The user must still have write access there.
func (s *Service) Restore(ctx context.Context, userID int64, a *Access, id string) (string, error) {
	item, err := s.Store.TrashItem(ctx, userID, id)
	if errors.Is(err, store.ErrNotFound) {
		return "", ErrNotFound
	} else if err != nil {
		return "", err
	}
	dir, err := s.FS.Authorize(a, Parent(item.OriginalPath), PermWrite)
	if err != nil {
		return "", err
	}
	if err := s.FS.MkdirAll(dir); err != nil {
		return "", err
	}
	to, err := s.FS.UniquePath(Join(dir, Base(item.OriginalPath)))
	if err != nil {
		return "", err
	}
	if err := s.FS.Rename(item.TrashPath, to); err != nil {
		return "", err
	}
	_ = s.FS.Root().Remove(Parent(item.TrashPath))
	s.InvalidateUsage(userID)
	return to, s.Store.DeleteTrash(ctx, id)
}

func (s *Service) Purge(ctx context.Context, userID int64, id string) error {
	item, err := s.Store.TrashItem(ctx, userID, id)
	if errors.Is(err, store.ErrNotFound) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	return s.purge(ctx, item)
}

func (s *Service) purge(ctx context.Context, item *store.TrashItem) error {
	if err := s.FS.RemoveAll(Parent(item.TrashPath)); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	return s.Store.DeleteTrash(ctx, item.ID)
}

func (s *Service) PurgeAll(ctx context.Context, userID int64) error {
	items, err := s.Store.TrashItems(ctx, userID)
	if err != nil {
		return err
	}
	for _, it := range items {
		if err := s.purge(ctx, it); err != nil {
			return err
		}
	}
	return nil
}

// PurgeOlderThan permanently deletes trash items trashed before cutoff.
func (s *Service) PurgeOlderThan(ctx context.Context, cutoff time.Time) (int, error) {
	items, err := s.Store.TrashOlderThan(ctx, cutoff)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, it := range items {
		if err := s.purge(ctx, it); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// Usage returns the bytes stored in the user's writable grants, cached for a
// few minutes. It is used for the optional per-user quota.
func (s *Service) Usage(userID int64, a *Access) int64 {
	s.usageMu.Lock()
	e, ok := s.usage[userID]
	s.usageMu.Unlock()
	if ok && time.Since(e.at) < 5*time.Minute {
		return e.bytes
	}
	var total int64
	var roots []string
	if a.Admin {
		roots = []string{""}
	} else {
		for _, g := range a.Grants {
			if g.Permission == "write" {
				roots = append(roots, g.Path)
			}
		}
	}
	for i, r := range roots {
		nested := false
		for j, o := range roots {
			if i != j && Within(r, o) && (r != o || j < i) {
				nested = true
			}
		}
		if !nested {
			n, _ := s.FS.Size(r)
			total += n
		}
	}
	s.usageMu.Lock()
	s.usage[userID] = usageEntry{bytes: total, at: time.Now()}
	s.usageMu.Unlock()
	return total
}

// AddUsage adjusts the cached usage after an upload.
func (s *Service) AddUsage(userID, n int64) {
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	if e, ok := s.usage[userID]; ok {
		e.bytes += n
		s.usage[userID] = e
	}
}

func (s *Service) InvalidateUsage(userID int64) {
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	delete(s.usage, userID)
}

// Reserve records n bytes for an in-flight upload, failing if the user's
// stored plus reserved bytes would exceed quota (quota <= 0: unlimited).
func (s *Service) Reserve(uploadID string, userID int64, a *Access, n, quota int64) bool {
	var used int64
	if quota > 0 {
		used = s.Usage(userID, a) // outside the lock: may walk the tree
	}
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	if quota > 0 {
		for _, r := range s.reserved {
			if r.userID == userID {
				used += r.bytes
			}
		}
		if used+n > quota {
			return false
		}
	}
	s.reserved[uploadID] = reservation{userID: userID, bytes: n, at: time.Now()}
	return true
}

// Release drops a reservation (upload finished, cancelled or expired).
func (s *Service) Release(uploadID string) {
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	delete(s.reserved, uploadID)
}

func (s *Service) ReleaseOlderThan(d time.Duration) {
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	for id, r := range s.reserved {
		if time.Since(r.at) > d {
			delete(s.reserved, id)
		}
	}
}
