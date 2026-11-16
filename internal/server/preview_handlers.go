package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"filebrowser/internal/files"
	"filebrowser/internal/httpx"
	"filebrowser/internal/store"
)

// fileInfo is what a client needs to choose a viewer: the type verified from
// the file's content and, for archives, whether a password is needed.
type fileInfo struct {
	files.Type
	// ExtMime is what the name claims, for telling the user about a mismatch.
	ExtMime string         `json:"ext_mime"`
	Archive *files.Archive `json:"archive,omitempty"`
}

// maxArchiveEntries bounds an archive index sent to a client.
const maxArchiveEntries = 5000

// inspect returns the verified type of the file at the real path p, cached
// while its size and modification time are unchanged.
func (s *Server) inspect(ctx context.Context, p string) (*fileInfo, error) {
	f, fi, err := s.files.FS.OpenFile(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	size, mtime := fi.Size(), fi.ModTime().UnixNano()
	if raw, err := s.store.FileType(ctx, p, size, mtime); err == nil {
		var info fileInfo
		if json.Unmarshal(raw, &info) == nil {
			return &info, nil
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	t, err := files.VerifyFile(fi.Name(), f)
	if err != nil {
		return nil, err
	}
	info := &fileInfo{Type: t, ExtMime: files.MimeType(fi.Name())}
	if files.IsArchive(t.Mime) {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		// Summary only (limit 0); a compressed file that is no archive has none.
		info.Archive, _ = files.InspectArchive(ctx, f, size, t.Mime, 0)
		cancel()
	}
	if raw, err := json.Marshal(info); err == nil {
		if err := s.store.PutFileType(context.WithoutCancel(ctx), p, size, mtime, raw); err != nil {
			slog.Warn("cache file type", "err", err)
		}
	}
	return info, nil
}

func (s *Server) handleFileInfo(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authorized(w, r, r.URL.Query().Get("path"), files.PermRead)
	if !ok {
		return
	}
	info, err := s.inspect(r.Context(), p)
	if err != nil {
		httpx.FileError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, info)
}

// handleArchiveIndex lists what an archive holds, without extracting it.
func (s *Server) handleArchiveIndex(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authorized(w, r, r.URL.Query().Get("path"), files.PermRead)
	if !ok {
		return
	}
	f, fi, err := s.files.FS.OpenFile(p)
	if err != nil {
		httpx.FileError(w, r, err)
		return
	}
	defer f.Close()
	t, err := files.VerifyFile(fi.Name(), f)
	if err != nil {
		httpx.FileError(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	a, err := files.InspectArchive(ctx, f, fi.Size(), t.Mime, maxArchiveEntries)
	if err != nil {
		httpx.Error(w, http.StatusUnprocessableEntity, "not a readable archive")
		return
	}
	httpx.JSON(w, http.StatusOK, a)
}
