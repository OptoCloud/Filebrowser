package server

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"filebrowser/internal/audit"
	"filebrowser/internal/files"
	"filebrowser/internal/httpx"
	"filebrowser/internal/media"
	"filebrowser/internal/shares"
)

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	id := httpx.IdentityFrom(r.Context())
	p, ok := httpx.QueryPath(w, r, "path")
	if !ok {
		return
	}
	entries, perm, err := s.files.FS.List(id.Access, p)
	if err != nil {
		httpx.FileError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"path":       p,
		"permission": perm.String(),
		"grant_root": id.Access.IsGrantRoot(p),
		"entries":    entries,
	})
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authorized(w, r, r.URL.Query().Get("path"), files.PermRead)
	if !ok {
		return
	}
	// PDF is never rendered inline on the app origin.
	serveFile(w, r, s.files.FS, p, fileOpts{inline: r.URL.Query().Get("inline") == "1"})
}

func (s *Server) handleZip(w http.ResponseWriter, r *http.Request) {
	id := httpx.IdentityFrom(r.Context())
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid form")
			return
		}
	}
	raw := r.Form["paths"]
	if r.Method == http.MethodGet {
		raw = r.URL.Query()["paths"]
	}
	paths, ok := httpx.CleanPaths(w, r, raw)
	if !ok {
		return
	}
	for i, p := range paths {
		p, err := s.files.FS.Authorize(id.Access, p, files.PermRead)
		if err != nil {
			httpx.FileError(w, r, err)
			return
		}
		paths[i] = p
		if _, err := s.files.FS.Stat(p); err != nil {
			httpx.FileError(w, r, err)
			return
		}
	}
	serveZip(w, r, s.files.FS, paths, func(real string) bool { return id.Access.Perm(real) >= files.PermRead })
}

func (s *Server) handleMkdir(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	id := httpx.IdentityFrom(r.Context())
	dir, ok := httpx.CleanPath(w, r, req.Path)
	if !ok {
		return
	}
	p, err := s.files.Mkdir(id.Access, dir, req.Name)
	if err != nil {
		httpx.FileError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"path": p})
}

func (s *Server) handleRename(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	id := httpx.IdentityFrom(r.Context())
	p, ok := httpx.CleanPath(w, r, req.Path)
	if !ok {
		return
	}
	to, err := s.files.Rename(id.Access, p, req.Name)
	if err != nil {
		httpx.FileError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"path": to})
}

func (s *Server) handleMove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Paths []string `json:"paths"`
		Dest  string   `json:"dest"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	id := httpx.IdentityFrom(r.Context())
	paths, ok := httpx.CleanPaths(w, r, req.Paths)
	if !ok {
		return
	}
	dest, ok := httpx.CleanPath(w, r, req.Dest)
	if !ok {
		return
	}
	moved, err := s.files.Move(id.Access, paths, dest)
	if err != nil {
		httpx.FileError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"paths": moved})
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Paths []string `json:"paths"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	id := httpx.IdentityFrom(ctx)
	paths, ok := httpx.CleanPaths(w, r, req.Paths)
	if !ok {
		return
	}
	items, err := s.files.Delete(ctx, id.User.ID, id.Access, paths)
	for _, it := range items {
		s.audit.Log(ctx, audit.FileDelete, it.OriginalPath, map[string]any{"trash_id": it.ID})
	}
	if err != nil {
		httpx.FileError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"trashed": len(items)})
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	id := httpx.IdentityFrom(r.Context())
	p, ok := httpx.QueryPath(w, r, "path")
	if !ok {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) < 2 || len(q) > 200 {
		httpx.Error(w, http.StatusBadRequest, "query must be 2-200 characters")
		return
	}
	res, truncated := s.files.FS.Search(r.Context(), id.Access, p, q, 500, 5*time.Second)
	if res == nil {
		res = []files.Entry{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"results": res, "truncated": truncated})
}

func (s *Server) serveThumb(w http.ResponseWriter, r *http.Request, p string) {
	if s.media == nil {
		http.NotFound(w, r)
		return
	}
	tp, err := s.media.Thumbnail(r.Context(), p)
	if errors.Is(err, media.ErrNoThumbnail) {
		w.Header().Set("Cache-Control", "private, max-age=3600")
		httpx.Error(w, http.StatusNotFound, "no thumbnail")
		return
	} else if err != nil {
		httpx.FileError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	serveCacheFile(w, r, tp)
}

func (s *Server) handleThumb(w http.ResponseWriter, r *http.Request) {
	if p, ok := s.authorized(w, r, r.URL.Query().Get("path"), files.PermRead); ok {
		s.serveThumb(w, r, p)
	}
}

// handlePreviewURL returns a short-lived signed URL on the share origin for
// previews that must not run on the app origin (PDF).
func (s *Server) handlePreviewURL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	p, fi, ok := s.readableFile(w, r, req.Path)
	if !ok {
		return
	}
	id := httpx.IdentityFrom(r.Context())
	link := shares.Link{UserID: id.User.ID, Session: id.Session.IDHash, Path: p, Inline: true, Expires: time.Now().Add(30 * time.Minute).Unix()}
	httpx.JSON(w, http.StatusOK, map[string]any{"url": s.shares.LinkURL(link, fi.Name())})
}

// Trash

func (s *Server) handleTrashList(w http.ResponseWriter, r *http.Request) {
	id := httpx.IdentityFrom(r.Context())
	items, err := s.store.TrashItems(r.Context(), id.User.ID)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "retention_days": s.cfg.Trash.RetentionDays})
}

func (s *Server) handleTrashRestore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	id := httpx.IdentityFrom(ctx)
	var restored []string
	for _, tid := range req.IDs {
		p, err := s.files.Restore(ctx, id.User.ID, id.Access, tid)
		if err != nil {
			httpx.FileError(w, r, err)
			return
		}
		s.audit.Log(ctx, audit.FileRestore, p, map[string]any{"trash_id": tid})
		restored = append(restored, p)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"paths": restored})
}

func (s *Server) handleTrashPurge(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
		All bool     `json:"all"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	id := httpx.IdentityFrom(ctx)
	if req.All {
		if err := s.files.PurgeAll(ctx, id.User.ID); err != nil {
			httpx.FileError(w, r, err)
			return
		}
		s.audit.Log(ctx, audit.FilePurge, "*", nil)
	}
	for _, tid := range req.IDs {
		if err := s.files.Purge(ctx, id.User.ID, tid); err != nil {
			httpx.FileError(w, r, err)
			return
		}
		s.audit.Log(ctx, audit.FilePurge, tid, nil)
	}
	httpx.OK(w)
}
