package server

import (
	"errors"
	"mime"
	"net/http"
	"slices"

	"filebrowser/internal/audit"
	"filebrowser/internal/files"
	"filebrowser/internal/httpx"
	"filebrowser/internal/shares"
	"filebrowser/internal/uploads"
)

// One-request uploads, for what cannot speak tus: curl and scripts (PUT),
// ShareX, iOS Shortcuts and HTML forms (multipart POST). Usually with an API
// token. A request body is as large as the proxy in front allows (100 MB
// behind Cloudflare); larger files go through tus.

type uploadedFile struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

func uploadError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, uploads.ErrTooLarge):
		httpx.Error(w, http.StatusRequestEntityTooLarge, "file too large")
	case errors.Is(err, uploads.ErrQuota):
		httpx.Error(w, http.StatusRequestEntityTooLarge, "quota exceeded")
	default:
		httpx.FileError(w, r, err)
	}
}

// handlePutFile stores the request body as the file named by the path. An
// existing file is not overwritten: the new one gets the next free name,
// which the response tells.
//
//	curl -T report.pdf -H "Authorization: Bearer fbt_..." https://files.example.com/api/v1/files/inbox/report.pdf
func (s *Server) handlePutFile(w http.ResponseWriter, r *http.Request) {
	p, ok := httpx.CleanPath(w, r, r.PathValue("path"))
	if !ok {
		return
	}
	if p == "" {
		httpx.Error(w, http.StatusBadRequest, "the path must name the file")
		return
	}
	final, n, err := s.uploads.Put(r.Context(), httpx.IdentityFrom(r.Context()), files.Parent(p), files.Base(p), r.Body)
	if err != nil {
		uploadError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, uploadedFile{Path: final, Name: files.Base(final), Size: n})
}

// handlePostUpload stores the files of a multipart form in the folder given
// as ?dir= (or a "dir" field sent before the files). With ?share=1 it also
// makes a download link for the upload and answers with its "url", which is
// what screenshot tools paste: that needs a session, or a token that may share.
func (s *Server) handlePostUpload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := httpx.IdentityFrom(ctx)
	share := r.URL.Query().Get("share") == "1"
	if share && id.Token != nil && !slices.Contains(id.Token.Caps, capShare) {
		httpx.ErrorCode(w, http.StatusForbidden, "token_scope", "this API token is not allowed to share")
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "expected a multipart form")
		return
	}
	dir := r.URL.Query().Get("dir")
	out := []uploadedFile{}
	for len(out) < 100 {
		part, err := mr.NextPart()
		if err != nil {
			break // the end of the form, or of the connection
		}
		name := part.FileName()
		if name == "" {
			if part.FormName() == "dir" && len(out) == 0 {
				buf := make([]byte, 4097)
				n, _ := part.Read(buf)
				dir = string(buf[:min(n, 4096)])
			}
			continue
		}
		// Browsers send the bare name; anything with a path in it is refused by Put.
		if dec, err := new(mime.WordDecoder).DecodeHeader(name); err == nil {
			name = dec
		}
		d, ok := httpx.CleanPath(w, r, dir)
		if !ok {
			return
		}
		final, n, err := s.uploads.Put(ctx, id, d, name, part)
		if err != nil {
			uploadError(w, r, err)
			return
		}
		out = append(out, uploadedFile{Path: final, Name: files.Base(final), Size: n})
	}
	if len(out) == 0 {
		httpx.Error(w, http.StatusBadRequest, "the form has no file")
		return
	}
	resp := map[string]any{"files": out}
	if share {
		// One file: a link to it. Several: nothing sensible to link to.
		if len(out) != 1 {
			httpx.Error(w, http.StatusBadRequest, "share=1 takes one file")
			return
		}
		sh, tok, err := s.shares.Create(ctx, id.User, id.Access, shares.CreateRequest{Path: out[0].Path})
		if err != nil {
			httpx.FileError(w, r, err)
			return
		}
		s.audit.Log(ctx, audit.ShareCreated, sh.Path, map[string]any{"share_id": sh.ID, "kind": sh.Kind, "expires_at": sh.ExpiresAt})
		resp["url"] = s.shares.URL(tok)
	}
	httpx.JSON(w, http.StatusCreated, resp)
}
