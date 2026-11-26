package server

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"filebrowser/internal/audit"
	"filebrowser/internal/auth"
	"filebrowser/internal/httpx"
	"filebrowser/internal/store"
)

func (s *Server) handleOIDCStart(w http.ResponseWriter, r *http.Request) {
	u, err := s.oidc.Start(r.Context(), w, r.URL.Query().Get("provider"), 0, nil)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "login provider unavailable")
		return
	}
	http.Redirect(w, r, u, http.StatusFound) // #nosec G710 nosemgrep -- u is the provider authorization URL built from server config
}

// handleOIDCLink starts linking a login provider's identity to the account.
// It changes state (the link in progress), so it is a POST that answers with
// the address to go to, not a link that can be followed from anywhere.
func (s *Server) handleOIDCLink(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider string `json:"provider"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	id := httpx.IdentityFrom(r.Context())
	// A linked identity is a sign-in method like a passkey: a stolen
	// session must not be able to add one (see requireFreshLogin).
	if !requireFreshLogin(w, id) {
		return
	}
	u, err := s.oidc.Start(r.Context(), w, req.Provider, id.User.ID, id.Session.IDHash)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "login provider unavailable")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"url": u})
}

func (s *Server) loginRedirect(w http.ResponseWriter, r *http.Request, errCode string) {
	http.Redirect(w, r, "/login?error="+url.QueryEscape(errCode), http.StatusFound)
}

func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ident, err := s.oidc.Callback(ctx, w, r)
	if err != nil {
		s.audit.LogUser(ctx, nil, audit.OIDCDenied, "", map[string]any{"error": err.Error()})
		s.loginRedirect(w, r, "oidc_failed")
		return
	}
	target := ident.Issuer + " " + ident.Subject
	if ident.LinkUserID > 0 {
		// The session that asked for the link must still stand: logging out,
		// revoking it or disabling the user while the provider's page was
		// open cancels the link.
		if !s.linkSessionValid(ctx, ident) {
			s.audit.LogUser(ctx, &ident.LinkUserID, audit.OIDCDenied, target, map[string]any{"reason": "link session ended"})
			s.loginRedirect(w, r, "oidc_failed")
			return
		}
		err := s.store.LinkOIDC(ctx, ident.LinkUserID, ident.Issuer, ident.Subject)
		if errors.Is(err, store.ErrConflict) {
			http.Redirect(w, r, "/settings?error=oidc_already_linked", http.StatusFound)
			return
		} else if err != nil {
			httpx.InternalError(w, r, err)
			return
		}
		s.audit.LogUser(ctx, &ident.LinkUserID, audit.OIDCLinked, target, nil)
		http.Redirect(w, r, "/settings?linked=1", http.StatusFound)
		return
	}
	// Accounts are linked by issuer + subject only, never by email.
	user, err := s.store.UserByOIDC(ctx, ident.Issuer, ident.Subject)
	if err != nil || user.Disabled {
		s.audit.LogUser(ctx, nil, audit.OIDCDenied, target, map[string]any{"reason": "not linked"})
		s.loginRedirect(w, r, "oidc_not_linked")
		return
	}
	trust := ident.MFA
	totp, keys, err := s.secondFactors(ctx, user)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	if !trust && (totp || keys > 0) {
		// The pending login goes in a cookie, not in the address, where it
		// would stay in the browser's history.
		auth.SetCookie(w, auth.Cookie{Name: s.pendingCookie(), Value: s.pending.add(user.ID, "oidc"), MaxAge: 300,
			SameSite: http.SameSiteStrictMode}, s.sessions.Secure)
		http.Redirect(w, r, "/login?mfa=cookie&methods="+strings.Join(methods(totp, keys), ","), http.StatusFound)
		return
	}
	if !s.startSession(w, r, user.ID, "oidc", trust) {
		return
	}
	s.audit.LogUser(ctx, &user.ID, audit.OIDCLogin, target, nil)
	http.Redirect(w, r, "/", http.StatusFound)
}

// linkSessionValid reports whether the session that started an account link
// still exists, is unexpired, recent enough to add a sign-in method, and
// belongs to the enabled user being linked.
func (s *Server) linkSessionValid(ctx context.Context, ident *auth.OIDCIdentity) bool {
	if len(ident.LinkSession) == 0 {
		return false
	}
	sess, err := s.store.SessionByHash(ctx, ident.LinkSession)
	if err != nil || sess.UserID != ident.LinkUserID || !s.sessions.Live(sess) ||
		time.Since(sess.CreatedAt) > freshLogin+10*time.Minute {
		return false
	}
	user, err := s.store.UserByID(ctx, sess.UserID)
	return err == nil && !user.Disabled
}

func (s *Server) handleOIDCUnlink(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Issuer  string `json:"issuer"`
		Subject string `json:"subject"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	id := httpx.IdentityFrom(r.Context())
	err := s.store.UnlinkOIDC(r.Context(), id.User.ID, req.Issuer, req.Subject)
	if errors.Is(err, store.ErrLastLogin) {
		httpx.ErrorCode(w, http.StatusConflict, "last_login_method", "set a password before unlinking your only login provider")
		return
	} else if err != nil {
		httpx.FileError(w, r, err)
		return
	}
	s.audit.Log(r.Context(), audit.OIDCUnlinked, req.Issuer+" "+req.Subject, nil)
	httpx.OK(w)
}
