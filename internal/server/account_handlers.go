package server

import (
	"bytes"
	"net/http"
	"time"

	"filebrowser/internal/audit"
	"filebrowser/internal/auth"
	"filebrowser/internal/httpx"
	"filebrowser/internal/store"
)

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	id := httpx.IdentityFrom(r.Context())
	idents, err := s.store.OIDCIdentities(r.Context(), id.User.ID)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	keys, err := s.store.WebAuthnCredentials(r.Context(), id.User.ID)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	resp := map[string]any{
		"user":           id.User,
		"grants":         id.Access.Grants,
		"can_write":      id.Access.CanWrite(),
		"totp_enabled":   id.User.TOTPEnabled(),
		"webauthn_count": len(keys),
		"mfa_enabled":    id.User.TOTPEnabled() || len(keys) > 0,
		"totp_required":  requiresTOTP(id),
		"restricted":     s.isRestricted(id),
		// The password was chosen by someone else: nothing else works until it is replaced.
		"must_change_password": id.User.MustChangePassword,
		"session_binding":      s.cfg.Session.Binding && id.User.SessionBinding,
		"session_binding_off":  !s.cfg.Session.Binding, // turned off for everyone
		"has_password":         id.User.PasswordHash != "",
		"oidc":                 idents,
		"providers":            s.oidc.Providers(),
		"share_origin":         s.cfg.ShareOrigin,
	}
	// Whether earlier versions of files can be asked for.
	resp["snapshots"] = len(s.snapshots()) > 0
	if s.r2 != nil {
		// Files from this size go through the upload bucket.
		resp["r2_threshold"] = s.cfg.R2.Threshold
	}
	if id.User.QuotaBytes > 0 {
		resp["usage_bytes"] = s.files.Usage(r.Context(), id.User.ID, id.Access)
	}
	httpx.JSON(w, http.StatusOK, resp)
}

// handleSessionBinding turns the binding of the user's sessions to their
// browser and network on or off.
func (s *Server) handleSessionBinding(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	id := httpx.IdentityFrom(r.Context())
	// Turning a protection off is not for a session that may be a copy.
	if !req.Enabled && !requireFreshLogin(w, id) {
		return
	}
	if err := s.store.SetSessionBinding(r.Context(), id.User.ID, req.Enabled); err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	s.audit.Log(r.Context(), audit.SessionMoved, id.User.Username, map[string]any{"binding": req.Enabled})
	httpx.OK(w)
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Current string `json:"current"`
		Code    string `json:"code"`
		New     string `json:"new"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	id := httpx.IdentityFrom(ctx)
	guess := s.loginBackoff.Slot(userKey("pw", id.User.ID))
	if d := auth.Reserve(time.Now(), guess); d > 0 {
		retryAfter(w, d)
		return
	}
	if id.User.PasswordHash != "" && !auth.VerifyPassword(id.User.PasswordHash, req.Current) {
		httpx.Error(w, http.StatusUnauthorized, "current password is wrong")
		return
	}
	// A first password (OIDC-only account) is a new long-lived credential:
	// require a second factor or a fresh login, not just a session.
	if id.User.PasswordHash == "" {
		switch {
		case id.User.TOTPEnabled():
			if !s.verifyTOTP(r, id.User, req.Code) {
				httpx.Error(w, http.StatusUnauthorized, "invalid authenticator code")
				return
			}
		case time.Since(id.Session.CreatedAt) > 10*time.Minute:
			guess.Refund() // nothing was guessed
			httpx.ErrorCode(w, http.StatusUnauthorized, "reauth_required", "log in again to set a password")
			return
		}
	}
	guess.Refund()
	if err := s.passwords.Check(ctx, req.New, id.User.Username); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	hash, err := auth.HashPassword(req.New)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	if err := s.store.SetPassword(ctx, id.User.ID, hash, false); err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	if !s.rotateSessions(w, r, id, id.Session.MFA) {
		return
	}
	s.audit.Log(ctx, audit.PasswordChanged, id.User.Username, nil)
	httpx.OK(w)
}

type sessionView struct {
	ID string `json:"id"`
	*store.Session
	Current  bool   `json:"current"`
	Username string `json:"username,omitempty"`
}

func (s *Server) handleMySessions(w http.ResponseWriter, r *http.Request) {
	id := httpx.IdentityFrom(r.Context())
	list, err := s.store.UserSessions(r.Context(), id.User.ID)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	out := make([]sessionView, 0, len(list))
	for _, sess := range list {
		out = append(out, sessionView{ID: auth.PublicID(sess.IDHash), Session: sess,
			Current: bytes.Equal(sess.IDHash, id.Session.IDHash)})
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (s *Server) handleDeleteMySession(w http.ResponseWriter, r *http.Request) {
	id := httpx.IdentityFrom(r.Context())
	hash, err := auth.ParsePublicID(r.PathValue("id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "not found")
		return
	}
	if err := s.store.DeleteUserSession(r.Context(), id.User.ID, hash); err != nil {
		httpx.FileError(w, r, err)
		return
	}
	httpx.OK(w)
}
