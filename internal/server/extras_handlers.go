package server

import (
	"context"
	"io/fs"
	"net/http"
	"slices"
	"sync"
	"time"

	"filebrowser/internal/files"
	"filebrowser/internal/httpx"
)

// Folder sizes

const (
	// sizeBudget is how long one request may spend adding up folders.
	sizeBudget = 5 * time.Second
	// sizeTTL is how long a folder's size is remembered.
	sizeTTL = 10 * time.Minute
	// sizeCacheMax bounds the remembered sizes.
	sizeCacheMax = 5000
)

type folderSize struct {
	bytes int64
	at    time.Time
}

// sizeCache remembers folder sizes by real path. A size is a property of
// the tree, not of who asks, and only those who may read the folder ask.
type sizeCache struct {
	mu sync.Mutex
	m  map[string]folderSize
}

func (c *sizeCache) get(p string) (int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[p]
	return e.bytes, ok && time.Since(e.at) < sizeTTL
}

func (c *sizeCache) put(p string, n int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) >= sizeCacheMax {
		c.m = map[string]folderSize{}
	}
	c.m[p] = folderSize{n, time.Now()}
}

// handleFolderSizes adds up the folders directly in a folder: what the
// listing cannot say without walking them. It answers with what it got
// done in sizeBudget; asked again, it goes on from what it remembers.
func (s *Server) handleFolderSizes(w http.ResponseWriter, r *http.Request) {
	id := httpx.IdentityFrom(r.Context())
	p, ok := httpx.QueryPath(w, r, "path")
	if !ok {
		return
	}
	entries, _, err := s.files.FS.List(id.Access, p)
	if err != nil {
		httpx.FileError(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), sizeBudget)
	defer cancel()
	sizes, partial := map[string]int64{}, false
	for _, e := range entries {
		if !e.IsDir {
			continue
		}
		// Only what the caller may read, by its real path: not a folder that
		// merely leads to one of their grants, nor a link out of them.
		real, err := s.files.FS.Authorize(id.Access, e.Path, files.PermRead)
		if err != nil {
			continue
		}
		if n, ok := s.sizes.get(real); ok {
			sizes[e.Name] = n
			continue
		}
		if ctx.Err() != nil {
			partial = true
			continue
		}
		n, whole := s.files.FS.SizeContext(ctx, real)
		if !whole {
			partial = true
			continue
		}
		s.sizes.put(real, n)
		sizes[e.Name] = n
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"sizes": sizes, "partial": partial})
}

// Earlier versions

// maxVersions bounds the snapshots looked through for one file.
const maxVersions = 500

type fileVersion struct {
	Snapshot string    `json:"snapshot"`
	Size     int64     `json:"size"`
	ModTime  time.Time `json:"mtime"`
}

// snapshots lists the snapshot names, newest name first; nil if there are none.
func (s *Server) snapshots() []string {
	if s.cfg.SnapshotsDir == "" {
		return nil
	}
	d, err := s.files.FS.Root().Open(s.cfg.SnapshotsDir)
	if err != nil {
		return nil
	}
	defer d.Close()
	names, _ := d.Readdirnames(-1)
	names = slices.DeleteFunc(names, func(n string) bool { return files.ValidComponent(n) != nil })
	slices.Sort(names)
	slices.Reverse(names)
	return names
}

// snapshotFile is the regular file that p (a real path) was in a snapshot.
// Nothing in a snapshot is followed: a link there could lead to what the
// caller may not read.
func (s *Server) snapshotFile(snapshot, p string) (string, fs.FileInfo, bool) {
	if files.ValidComponent(snapshot) != nil || p == "" {
		return "", nil, false
	}
	full := files.Join(files.Join(s.cfg.SnapshotsDir, snapshot), p)
	fi, err := s.files.FS.Root().Lstat(full)
	if err != nil || !fi.Mode().IsRegular() {
		return "", nil, false
	}
	// The directories on the way are the snapshot's own, not links.
	for dir := files.Parent(p); dir != ""; dir = files.Parent(dir) {
		di, err := s.files.FS.Root().Lstat(files.Join(files.Join(s.cfg.SnapshotsDir, snapshot), dir))
		if err != nil || !di.IsDir() {
			return "", nil, false
		}
	}
	return full, fi, true
}

// versionPath authorizes reading the versions of the file named by ?path=,
// which may itself be gone: the right to read it is the right to read where
// it was.
func (s *Server) versionPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	p, ok := httpx.QueryPath(w, r, "path")
	if !ok {
		return "", false
	}
	id := httpx.IdentityFrom(r.Context())
	real, err := s.files.FS.Authorize(id.Access, p, files.PermRead)
	if err != nil {
		// Deleted since: its folder still says who may read there.
		dir, derr := s.files.FS.Authorize(id.Access, files.Parent(p), files.PermRead)
		if derr != nil || files.Need(id.Access, p, files.PermRead) != nil {
			httpx.FileError(w, r, err)
			return "", false
		}
		real = files.Join(dir, files.Base(p))
	}
	return real, true
}

// handleVersions lists the earlier versions of a file that the snapshots
// hold: each state of it once, newest snapshot first.
func (s *Server) handleVersions(w http.ResponseWriter, r *http.Request) {
	p, ok := s.versionPath(w, r)
	if !ok {
		return
	}
	out := []fileVersion{}
	var last fileVersion
	if fi, err := s.files.FS.Stat(p); err == nil {
		last = fileVersion{Size: fi.Size(), ModTime: fi.ModTime()} // the file as it is now is no earlier version
	}
	names := s.snapshots()
	for _, name := range names[:min(len(names), maxVersions)] {
		_, fi, ok := s.snapshotFile(name, p)
		if !ok || (fi.Size() == last.Size && fi.ModTime().Equal(last.ModTime)) {
			continue
		}
		last = fileVersion{Snapshot: name, Size: fi.Size(), ModTime: fi.ModTime().UTC()}
		out = append(out, last)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"versions": out})
}

// handleVersionDownload serves a file as it was in a snapshot.
func (s *Server) handleVersionDownload(w http.ResponseWriter, r *http.Request) {
	p, ok := s.versionPath(w, r)
	if !ok {
		return
	}
	full, _, ok := s.snapshotFile(r.URL.Query().Get("snapshot"), p)
	if !ok {
		httpx.Error(w, http.StatusNotFound, "not found")
		return
	}
	f, err := s.files.FS.Root().Open(full)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "not found")
		return
	}
	httpx.UserContentHeaders(w, "application/octet-stream", false, files.Base(p))
	serveOpened(w, r, f)
}
