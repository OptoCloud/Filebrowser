package server

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"filebrowser/internal/audit"
	"filebrowser/internal/auth"
	"filebrowser/internal/files"
	"filebrowser/internal/httpx"
	"filebrowser/internal/store"
	"filebrowser/internal/webhooks"
)

// hookEvents maps what the audit log records to what webhooks are told.
var hookEvents = map[string]string{
	audit.FileUpload:   webhooks.FileUploaded,
	audit.ShareUpload:  webhooks.DropReceived,
	audit.ShareCreated: webhooks.ShareCreated,
	audit.ShareAccess:  webhooks.ShareAccessed,
}

// notifyHooks passes an audited event on to the webhooks of the user it is
// about (for a share, its owner).
func (s *Server) notifyHooks(ctx context.Context, userID *int64, action, target string, details any) {
	s.count(action, details)
	if event, ok := hookEvents[action]; ok && userID != nil {
		s.hooks.Emit(ctx, *userID, event, target, details)
	}
}

// count keeps the counters that follow from audited events.
func (s *Server) count(action string, details any) {
	d, _ := details.(map[string]any)
	switch action {
	case audit.FileUpload, audit.ShareUpload:
		via := "app"
		if action == audit.ShareUpload {
			via = "drop"
		}
		s.Metrics.Add(1, "filebrowser_uploads_total", "via", via)
		switch n := d["size"].(type) {
		case int64:
			s.Metrics.Add(n, "filebrowser_upload_bytes_total", "via", via)
		case int:
			s.Metrics.Add(int64(n), "filebrowser_upload_bytes_total", "via", via)
		}
	case audit.LoginOK, audit.OIDCLogin:
		s.Metrics.Add(1, "filebrowser_logins_total", "result", "ok")
	case audit.LoginFailed, audit.TOTPFailed, audit.WebAuthnFailed, audit.OIDCDenied:
		s.Metrics.Add(1, "filebrowser_logins_total", "result", "failed")
	case audit.ShareAccess:
		s.Metrics.Add(1, "filebrowser_share_downloads_total")
	case audit.ShareCreated:
		s.Metrics.Add(1, "filebrowser_shares_created_total")
	}
}

func (s *Server) handleListWebhooks(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.Webhooks(r.Context(), httpx.IdentityFrom(r.Context()).User.ID)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	if list == nil {
		list = []*store.Webhook{}
	}
	httpx.JSON(w, http.StatusOK, list)
}

func (s *Server) handleCreateWebhook(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string   `json:"name"`
		URL    string   `json:"url"`
		Events []string `json:"events"`
		Path   string   `json:"path"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	id := httpx.IdentityFrom(ctx)
	// A webhook makes the server send what happens to the user's files
	// elsewhere, for as long as it exists: not for a stolen session to set up.
	if !requireFreshLogin(w, id) {
		return
	}
	bad := func(msg string) { httpx.Error(w, http.StatusBadRequest, msg) }
	req.Name, req.URL = strings.TrimSpace(req.Name), strings.TrimSpace(req.URL)
	if req.Name == "" || utf8.RuneCountInString(req.Name) > 60 {
		bad("a webhook needs a name of at most 60 characters")
		return
	}
	slices.Sort(req.Events)
	req.Events = slices.Compact(req.Events)
	if len(req.Events) == 0 || slices.ContainsFunc(req.Events, func(e string) bool { return !slices.Contains(webhooks.Events, e) }) {
		bad("choose the events to be told about")
		return
	}
	if err := webhooks.CheckURL(req.URL, id.User.IsAdmin()); err != nil {
		bad(err.Error())
		return
	}
	p, ok := httpx.CleanPath(w, r, req.Path)
	if !ok {
		return
	}
	if p != "" {
		var err error
		if p, err = s.files.FS.Authorize(id.Access, p, files.PermRead); err != nil {
			httpx.FileError(w, r, err)
			return
		}
	}
	have, err := s.store.Webhooks(ctx, id.User.ID)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	if len(have) >= webhooks.MaxPerUser {
		bad("too many webhooks: delete one first")
		return
	}
	secret := auth.NewToken(32)
	enc, err := s.keys.Encrypt([]byte(secret), []byte("webhook"))
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	h := &store.Webhook{UserID: id.User.ID, Name: req.Name, URL: req.URL, Secret: enc, Events: req.Events, Path: p}
	if err := s.store.CreateWebhook(ctx, h); err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	// The address is not logged: it often carries the receiver's own secret.
	s.audit.Log(ctx, audit.WebhookCreated, h.Name, map[string]any{"id": h.ID, "events": h.Events, "path": h.Path})
	// The secret is shown this once, for the receiver to check signatures with.
	httpx.JSON(w, http.StatusOK, map[string]any{"webhook": h, "secret": secret})
}

func (s *Server) handleDeleteWebhook(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteWebhook(r.Context(), httpx.IdentityFrom(r.Context()).User.ID, id); err != nil {
		httpx.FileError(w, r, err)
		return
	}
	s.audit.Log(r.Context(), audit.WebhookDeleted, "", map[string]any{"id": id})
	httpx.OK(w)
}

// handleTestWebhook sends a "ping" to a webhook; the outcome shows in its
// last delivery.
func (s *Server) handleTestWebhook(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	me := httpx.IdentityFrom(r.Context())
	h, err := s.store.Webhook(r.Context(), me.User.ID, id)
	if err != nil {
		httpx.FileError(w, r, err)
		return
	}
	s.hooks.Test(h, me.User.IsAdmin())
	httpx.OK(w)
}
