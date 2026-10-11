package server

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"filebrowser/internal/files"
	"filebrowser/internal/httpx"
	"filebrowser/internal/media"
	"filebrowser/internal/shares"
)

// mediaPath reads ?path=, requiring read access and media support.
func (s *Server) mediaPath(w http.ResponseWriter, r *http.Request, p string) (string, *httpx.Identity, bool) {
	if s.media == nil {
		httpx.Error(w, http.StatusNotImplemented, "media support disabled")
		return "", nil, false
	}
	real, ok := s.authorized(w, r, p, files.PermRead)
	return real, httpx.IdentityFrom(r.Context()), ok
}

func mediaError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, media.ErrBusy) {
		w.Header().Set("Retry-After", "30")
		httpx.ErrorCode(w, http.StatusTooManyRequests, "busy", "too many media jobs, try again shortly")
		return
	}
	if errors.Is(err, media.ErrConvert) {
		slog.WarnContext(r.Context(), "media conversion failed", "err", err)
		httpx.ErrorCode(w, http.StatusUnprocessableEntity, "convert_failed", "could not convert this file")
		return
	}
	httpx.FileError(w, r, err)
}

func (s *Server) handleMediaInfo(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p, id, ok := s.mediaPath(w, r, q.Get("path"))
	if !ok {
		return
	}
	probe, err := s.media.Probe(r.Context(), p)
	if err != nil {
		if errors.Is(err, files.ErrNotFound) || errors.Is(err, files.ErrIsDir) {
			httpx.FileError(w, r, err)
			return
		}
		httpx.ErrorCode(w, http.StatusUnprocessableEntity, "probe_failed", "could not read media file")
		return
	}
	audio := -1
	if v := q.Get("audio"); v != "" {
		audio, _ = strconv.Atoi(v)
	}
	plan := media.Decide(probe, p, media.ParseCaps(q.Get("caps")), audio)
	plan.Subtitles = append(plan.Subtitles, s.media.Sidecars(id.Access, p)...)
	resp := map[string]any{"plan": plan}
	if plan.Variant != "" {
		job, err := s.media.JobStatus(r.Context(), p, plan.Variant)
		if err == nil {
			resp["job"] = job
		}
	}
	httpx.JSON(w, http.StatusOK, resp)
}

func (s *Server) handleMediaPrepare(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path    string `json:"path"`
		Variant string `json:"variant"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	p, id, ok := s.mediaPath(w, r, req.Path)
	if !ok {
		return
	}
	job, err := s.media.Prepare(r.Context(), id.User.ID, p, req.Variant)
	if err != nil {
		mediaError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, job)
}

func (s *Server) handleMediaJob(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.mediaPath(w, r, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	job, err := s.media.JobStatus(r.Context(), p, r.URL.Query().Get("variant"))
	if err != nil {
		mediaError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, job)
}

func (s *Server) handleMediaStream(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.mediaPath(w, r, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	f, err := s.media.OpenVariant(r.Context(), p, r.URL.Query().Get("variant"))
	if err != nil {
		mediaError(w, r, err)
		return
	}
	httpx.UserContentHeaders(w, "video/mp4", true, files.Base(p)+".mp4")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	serveOpened(w, r, f)
}

// handleHLSPlaylist serves the live HLS playlist of a variant. Segment URLs
// are relative to it.
func (s *Server) handleHLSPlaylist(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p, id, ok := s.mediaPath(w, r, q.Get("path"))
	if !ok {
		return
	}
	variant := q.Get("variant")
	base := url.Values{"path": {q.Get("path")}, "variant": {variant}}
	seg := func(n int) string {
		v := url.Values{"path": base["path"], "variant": base["variant"], "n": {strconv.Itoa(n)}}
		return "segment?" + v.Encode()
	}
	pl, err := s.media.HLSPlaylist(r.Context(), id.User.ID, p, variant, "init?"+base.Encode(), seg)
	if err != nil {
		mediaError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(pl) // #nosec G705 -- an HLS playlist, not HTML: nosniff and a sandbox CSP
}

func (s *Server) handleHLSInit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p, id, ok := s.mediaPath(w, r, q.Get("path"))
	if !ok {
		return
	}
	f, err := s.media.HLSInit(r.Context(), id.User.ID, p, q.Get("variant"))
	if err != nil {
		mediaError(w, r, err)
		return
	}
	serveHLSPart(w, r, f, "init.mp4")
}

func (s *Server) handleHLSSegment(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p, id, ok := s.mediaPath(w, r, q.Get("path"))
	if !ok {
		return
	}
	n, err := strconv.Atoi(q.Get("n"))
	if err != nil || n < 0 {
		httpx.FileError(w, r, files.ErrBadPath)
		return
	}
	f, err := s.media.HLSSegment(r.Context(), id.User.ID, p, q.Get("variant"), n)
	if err != nil {
		mediaError(w, r, err)
		return
	}
	serveHLSPart(w, r, f, strconv.Itoa(n)+".m4s")
}

func serveHLSPart(w http.ResponseWriter, r *http.Request, f *os.File, name string) {
	httpx.UserContentHeaders(w, "video/mp4", true, name)
	// Not cached: the URL stays the same when the file changes.
	w.Header().Set("Cache-Control", "private, no-cache")
	serveOpened(w, r, f)
}

func (s *Server) handleMediaSubtitle(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p, id, ok := s.mediaPath(w, r, q.Get("path"))
	if !ok {
		return
	}
	if fi, err := s.files.FS.Stat(p); err != nil || !fi.Mode().IsRegular() {
		httpx.FileError(w, r, files.ErrNotFound)
		return
	}
	src, track := p, -1
	if sc := q.Get("sidecar"); sc != "" {
		if files.ValidName(sc) != nil || !media.IsSidecar(sc) {
			httpx.FileError(w, r, files.ErrBadPath)
			return
		}
		// The sidecar must be readable in its own right.
		real, err := s.files.FS.Authorize(id.Access, files.Join(files.Parent(p), sc), files.PermRead)
		if err != nil {
			httpx.FileError(w, r, files.ErrNotFound)
			return
		}
		src = real
	} else {
		n, err := strconv.Atoi(q.Get("track"))
		if err != nil || n < 0 {
			httpx.FileError(w, r, files.ErrBadPath)
			return
		}
		track = n
	}
	vtt, err := s.media.Subtitle(r.Context(), src, track)
	if err != nil {
		mediaError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	serveCacheFile(w, r, vtt)
}

func (s *Server) handleExternalURL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path    string `json:"path"`
		Variant string `json:"variant"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	p, fi, ok := s.readableFile(w, r, req.Path)
	if !ok {
		return
	}
	id := httpx.IdentityFrom(r.Context())
	ttl := s.cfg.Media.ExternalURLTTL.Duration
	link := shares.Link{UserID: id.User.ID, Session: id.Session.IDHash, Path: p, Variant: req.Variant, Expires: time.Now().Add(ttl).Unix()}
	httpx.JSON(w, http.StatusOK, map[string]any{"url": s.shares.LinkURL(link, fi.Name()), "expires_at": time.Unix(link.Expires, 0).UTC()})
}
