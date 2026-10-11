package server

import (
	"errors"
	"net/http"
	"time"

	"filebrowser/internal/audit"
	"filebrowser/internal/files"
	"filebrowser/internal/httpx"
	"filebrowser/internal/shares"
	"filebrowser/internal/store"
)

type shareView struct {
	*store.Share
	Name   string `json:"name"`
	Active bool   `json:"active"`
	HasPW  bool   `json:"has_password"`
	Owner  string `json:"owner,omitempty"`
}

func viewShare(sh *store.Share) shareView {
	name := files.Base(sh.Path)
	if name == "" {
		name = "/"
	}
	return shareView{Share: sh, Name: name, HasPW: sh.PasswordHash != "",
		Active: sh.RevokedAt == nil && time.Now().Before(sh.ExpiresAt) &&
			(sh.MaxDownloads == 0 || sh.DownloadCount < sh.MaxDownloads)}
}

func (s *Server) handleListShares(w http.ResponseWriter, r *http.Request) {
	id := httpx.IdentityFrom(r.Context())
	owner := id.User.ID
	all := r.URL.Query().Get("all") == "1" && id.User.IsAdmin()
	if all {
		owner = 0
	}
	list, err := s.store.Shares(r.Context(), owner)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	names := map[int64]string{}
	if all {
		users, err := s.store.ListUsers(r.Context())
		if err != nil {
			httpx.InternalError(w, r, err)
			return
		}
		for _, u := range users {
			names[u.ID] = u.Username
		}
	}
	out := make([]shareView, 0, len(list))
	for _, sh := range list {
		v := viewShare(sh)
		v.Owner = names[sh.OwnerID]
		out = append(out, v)
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateShare(w http.ResponseWriter, r *http.Request) {
	var req shares.CreateRequest
	if !httpx.Decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	id := httpx.IdentityFrom(ctx)
	sh, token, err := s.shares.Create(ctx, id.User, id.Access, req)
	if err != nil {
		httpx.FileError(w, r, err)
		return
	}
	s.audit.Log(ctx, audit.ShareCreated, sh.Path, map[string]any{"share_id": sh.ID, "kind": sh.Kind,
		"expires_at": sh.ExpiresAt, "password": sh.PasswordHash != "", "max_downloads": sh.MaxDownloads})
	httpx.JSON(w, http.StatusOK, map[string]any{"share": viewShare(sh), "url": s.shares.URL(token)})
}

func (s *Server) handleRevokeShare(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := httpx.IdentityFrom(ctx)
	sid, ok := parseID(w, r)
	if !ok {
		return
	}
	sh, err := s.store.ShareByID(ctx, sid)
	if err != nil || (sh.OwnerID != id.User.ID && !id.User.IsAdmin()) {
		httpx.Error(w, http.StatusNotFound, "not found")
		return
	}
	if err := s.store.RevokeShare(ctx, sid, time.Now()); err != nil && !errors.Is(err, store.ErrNotFound) {
		httpx.InternalError(w, r, err)
		return
	}
	s.audit.Log(ctx, audit.ShareRevoked, sh.Path, map[string]any{"share_id": sid})
	httpx.JSON(w, http.StatusOK, map[string]any{"ok": true})
}

// Share origin (public)

// resolveShare validates the token with per-IP rate limiting on lookups.
// requireUnlocked also enforces the password.
func (s *Server) resolveShare(w http.ResponseWriter, r *http.Request, requireUnlocked bool) *shares.Resolved {
	ctx := r.Context()
	if !s.shareLimiter.Allow(httpx.IP(ctx), time.Now()) {
		retryAfter(w, time.Minute)
		return nil
	}
	res, err := s.shares.Resolve(ctx, r.PathValue("token"))
	if errors.Is(err, shares.ErrInvalid) {
		httpx.Error(w, http.StatusNotFound, "this link is invalid or has expired")
		return nil
	} else if err != nil {
		httpx.InternalError(w, r, err)
		return nil
	}
	if requireUnlocked && !s.shares.Unlocked(r, res.Share) {
		httpx.ErrorCode(w, http.StatusUnauthorized, "password_required", "password required")
		return nil
	}
	return res
}

func (s *Server) handleShareInfo(w http.ResponseWriter, r *http.Request) {
	res := s.resolveShare(w, r, false)
	if res == nil {
		return
	}
	sh := res.Share
	info := map[string]any{
		"name":           files.Base(sh.Path),
		"kind":           sh.Kind,
		"is_dir":         sh.IsDir,
		"expires_at":     sh.ExpiresAt,
		"needs_password": sh.PasswordHash != "",
		"unlocked":       s.shares.Unlocked(r, sh),
	}
	if !sh.IsDir && info["unlocked"] == true {
		if fi, err := s.files.FS.Stat(res.Root); err == nil {
			info["size"] = fi.Size()
			info["mime"] = files.MimeType(fi.Name())
		}
	}
	if sh.MaxDownloads > 0 {
		info["downloads_left"] = max(sh.MaxDownloads-sh.DownloadCount, 0)
	}
	httpx.JSON(w, http.StatusOK, info)
}

func (s *Server) handleShareUnlock(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	res := s.resolveShare(w, r, false)
	if res == nil {
		return
	}
	ctx := r.Context()
	key := "share:" + httpx.IP(ctx)
	if d := s.loginBackoff.Wait(key, time.Now()); d > 0 {
		retryAfter(w, d)
		return
	}
	if !s.shares.Unlock(w, res.Share, req.Password) {
		s.loginBackoff.Fail(key, time.Now())
		s.audit.LogUser(ctx, nil, audit.ShareUnlockFail, res.Share.Path, map[string]any{"share_id": res.Share.ID})
		httpx.Error(w, http.StatusUnauthorized, "wrong password")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleShareList(w http.ResponseWriter, r *http.Request) {
	res := s.resolveShare(w, r, true)
	if res == nil {
		return
	}
	if res.Share.Kind != "download" || !res.Share.IsDir {
		httpx.Error(w, http.StatusNotFound, "not found")
		return
	}
	p, err := res.Sub(r.URL.Query().Get("path"))
	if err != nil {
		httpx.FileError(w, r, err)
		return
	}
	entries, _, err := s.files.FS.List(res.Access, p)
	if err != nil {
		httpx.FileError(w, r, err)
		return
	}
	visible := entries[:0]
	for _, e := range entries {
		// Hide symlinks that lead outside the share.
		if real, err := s.files.FS.Resolve(e.Path); err == nil && res.Allow(real) {
			e.Path = res.Rel(e.Path)
			visible = append(visible, e)
		}
	}
	entries = visible
	httpx.JSON(w, http.StatusOK, map[string]any{"path": res.Rel(p), "entries": entries})
}

// countDownload counts a download against the share limit. Each browser is
// counted once per share (tracked with a signed cookie), so previewing and then
// downloading, seeking in a video or resuming uses one download. Any other
// request, whatever its Range header, is a new download.
func (s *Server) countDownload(w http.ResponseWriter, r *http.Request, res *shares.Resolved, target string) bool {
	if s.shares.Counted(r, res.Share) {
		return true
	}
	ok, err := s.store.CountShareDownload(r.Context(), res.Share.ID)
	if err != nil {
		httpx.InternalError(w, r, err)
		return false
	}
	if !ok {
		httpx.Error(w, http.StatusGone, "download limit reached")
		return false
	}
	s.shares.MarkCounted(w, res.Share)
	s.audit.LogUser(r.Context(), &res.Owner.ID, audit.ShareAccess, target, map[string]any{"share_id": res.Share.ID})
	return true
}

func (s *Server) handleShareFile(w http.ResponseWriter, r *http.Request) {
	res := s.resolveShare(w, r, true)
	if res == nil {
		return
	}
	if res.Share.Kind != "download" {
		httpx.Error(w, http.StatusNotFound, "not found")
		return
	}
	p, err := res.Sub(r.URL.Query().Get("path"))
	if err != nil {
		httpx.FileError(w, r, err)
		return
	}
	if fi, err := s.files.FS.Stat(p); err != nil {
		httpx.FileError(w, r, err)
		return
	} else if fi.IsDir() {
		httpx.FileError(w, r, files.ErrIsDir)
		return
	}
	if r.Method == http.MethodGet && !s.countDownload(w, r, res, p) {
		return
	}
	inline := r.URL.Query().Get("inline") == "1"
	if inline && files.MimeType(p) == "application/pdf" {
		// Allow the share page itself to frame the PDF preview.
		serveFramablePDF(w, r, s.files.FS, p, "'self'")
		return
	}
	serveFile(w, r, s.files.FS, p, inline, true)
}

func serveFramablePDF(w http.ResponseWriter, r *http.Request, fsys *files.FS, p, ancestors string) {
	f, fi, err := fsys.OpenFile(p)
	if err != nil {
		httpx.FileError(w, r, err)
		return
	}
	defer f.Close()
	httpx.UserContentHeaders(w, "application/pdf", true, fi.Name())
	w.Header().Del("X-Frame-Options")
	w.Header().Set("Content-Security-Policy", w.Header().Get("Content-Security-Policy")+"; frame-ancestors "+ancestors)
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "private, no-cache")
	}
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

func (s *Server) handleShareZip(w http.ResponseWriter, r *http.Request) {
	res := s.resolveShare(w, r, true)
	if res == nil {
		return
	}
	if res.Share.Kind != "download" {
		httpx.Error(w, http.StatusNotFound, "not found")
		return
	}
	var paths []string
	raw := r.URL.Query()["paths"]
	if len(raw) == 0 {
		raw = []string{""}
	}
	for _, rp := range raw {
		p, err := res.Sub(rp)
		if err != nil {
			httpx.FileError(w, r, err)
			return
		}
		if _, err := s.files.FS.Stat(p); err != nil {
			httpx.FileError(w, r, err)
			return
		}
		paths = append(paths, p)
	}
	if !s.countDownload(w, r, res, res.Share.Path) {
		return
	}
	httpx.UserContentHeaders(w, "application/zip", false, zipName(paths))
	w.Header().Set("Cache-Control", "no-store")
	if err := s.files.FS.WriteZip(r.Context(), w, paths, res.Allow); err != nil {
		panic(http.ErrAbortHandler)
	}
}

func (s *Server) handleShareThumb(w http.ResponseWriter, r *http.Request) {
	res := s.resolveShare(w, r, true)
	if res == nil {
		return
	}
	if res.Share.Kind != "download" {
		httpx.Error(w, http.StatusNotFound, "not found")
		return
	}
	p, err := res.Sub(r.URL.Query().Get("path"))
	if err != nil {
		httpx.FileError(w, r, err)
		return
	}
	s.serveThumb(w, r, p)
}

// handleSignedLink serves a file via a short-lived HMAC-signed URL (external
// players, PDF previews). The user's access is re-checked on every request.
func (s *Server) handleSignedLink(w http.ResponseWriter, r *http.Request) {
	// Bearer-token URLs must never be stored by browsers or intermediaries.
	w.Header().Set("Cache-Control", "private, no-store")
	ctx := r.Context()
	if !s.shareLimiter.Allow(httpx.IP(ctx), time.Now()) {
		retryAfter(w, time.Minute)
		return
	}
	link, err := s.shares.VerifyLink(r.PathValue("token"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "link expired")
		return
	}
	user, err := s.store.UserByID(ctx, link.UserID)
	if err != nil || user.Disabled {
		httpx.Error(w, http.StatusNotFound, "link expired")
		return
	}
	grants, err := s.store.Grants(ctx, user.ID)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	a := &files.Access{Admin: user.IsAdmin(), Grants: grants}
	p, err := files.Clean(link.Path)
	if err == nil {
		p, err = s.files.FS.Authorize(a, p, files.PermRead)
	}
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "link expired")
		return
	}
	if link.Variant != "" {
		if s.media == nil {
			http.NotFound(w, r)
			return
		}
		f, err := s.media.OpenVariant(ctx, p, link.Variant)
		if err != nil {
			httpx.FileError(w, r, err)
			return
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			httpx.InternalError(w, r, err)
			return
		}
		httpx.UserContentHeaders(w, "video/mp4", true, files.Base(p)+".mp4")
		http.ServeContent(w, r, "", fi.ModTime(), f)
		return
	}
	if link.Inline && files.MimeType(p) == "application/pdf" {
		serveFramablePDF(w, r, s.files.FS, p, s.cfg.AppOrigin)
		return
	}
	serveFile(w, r, s.files.FS, p, link.Inline, true)
}
