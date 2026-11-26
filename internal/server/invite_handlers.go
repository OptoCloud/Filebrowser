package server

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"filebrowser/internal/audit"
	"filebrowser/internal/auth"
	"filebrowser/internal/files"
	"filebrowser/internal/httpx"
	"filebrowser/internal/store"
)

func (s *Server) lookupInvite(w http.ResponseWriter, r *http.Request, token string) *store.Invite {
	if !s.inviteLimit.Allow(httpx.LimitKey(r.Context()), time.Now()) {
		retryAfter(w, time.Minute)
		return nil
	}
	if len(token) < 20 || len(token) > 64 {
		httpx.Error(w, http.StatusNotFound, "invite not found or expired")
		return nil
	}
	inv, err := s.store.InviteByTokenHash(r.Context(), s.keys.HashToken(token))
	if err != nil || inv.UsedAt != nil || time.Now().After(inv.ExpiresAt) {
		httpx.Error(w, http.StatusNotFound, "invite not found or expired")
		return nil
	}
	return inv
}

func (s *Server) handleInviteInfo(w http.ResponseWriter, r *http.Request) {
	inv := s.lookupInvite(w, r, r.URL.Query().Get("token"))
	if inv == nil {
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"role": inv.Role, "expires_at": inv.ExpiresAt})
}

func (s *Server) handleInviteAccept(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token    string `json:"token"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	inv := s.lookupInvite(w, r, req.Token)
	if inv == nil {
		return
	}
	if !ValidUsername(req.Username) {
		httpx.Error(w, http.StatusBadRequest, "username may contain letters, digits, '.', '_' and '-' (max 64)")
		return
	}
	if err := s.passwords.Check(r.Context(), req.Password, req.Username); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	ctx := r.Context()
	user, err := s.store.AcceptInvite(ctx, inv.ID, req.Username, hash, time.Now())
	switch {
	case errors.Is(err, store.ErrConflict):
		httpx.Error(w, http.StatusConflict, "username is taken")
		return
	case errors.Is(err, store.ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "invite not found or expired")
		return
	case err != nil:
		httpx.InternalError(w, r, err)
		return
	}
	s.audit.LogUser(ctx, &user.ID, audit.InviteAccepted, user.Username, map[string]any{"invite_id": inv.ID})
	if s.startSession(w, r, user.ID, "invite", false) {
		httpx.OK(w)
	}
}

// validGrantPath cleans an admin-supplied grant path. The whole tree must be
// requested explicitly as "/", so a blank field never grants everything.
func validGrantPath(p string) (string, bool) {
	if strings.TrimSpace(p) == "" {
		return "", false
	}
	c, err := files.Clean(p)
	return c, err == nil
}
