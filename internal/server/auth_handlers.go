package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"filebrowser/internal/audit"
	"filebrowser/internal/auth"
	"filebrowser/internal/httpx"
	"filebrowser/internal/store"
)

var usernameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func ValidUsername(u string) bool { return usernameRe.MatchString(u) }

// handleHealth reports whether the server can reach its database. It is used
// by the updater after a restart and reveals nothing else.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleAuthConfig(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, http.StatusOK, map[string]any{
		"oidc":         s.oidc.Providers(),
		"share_origin": s.cfg.ShareOrigin,
	})
}

func retryAfter(w http.ResponseWriter, d time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(int(d.Seconds())+1))
	httpx.ErrorCode(w, http.StatusTooManyRequests, "rate_limited", "too many attempts, try again later")
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	now := time.Now()
	// Checked before the name becomes a rate-limit key: the keys are kept for
	// an hour, so megabyte-long names would otherwise pin memory.
	if len(req.Username) > 64 {
		req.Username = "" // no such user; the attempt still counts against the IP
	}
	// Hard backoff per IP and per IP+account. The account-wide limit is soft
	// (short cap) so that failures from elsewhere cannot lock the owner out.
	ip := s.loginBackoff.Slot("ip:" + httpx.LimitKey(ctx))
	pair := s.loginBackoff.Slot("ipuser:" + httpx.LimitKey(ctx) + "|" + strings.ToLower(req.Username))
	account := s.accountBackoff.Slot("user:" + strings.ToLower(req.Username))
	if d := auth.Reserve(now, ip, pair, account); d > 0 {
		retryAfter(w, d)
		return
	}
	if len(req.Password) > auth.MaxPasswordLen {
		httpx.Error(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	user, err := s.store.UserByName(ctx, req.Username)
	ok := false
	switch {
	case err == nil && !user.Disabled:
		ok = auth.VerifyPassword(user.PasswordHash, req.Password)
	case err == nil || errors.Is(err, store.ErrNotFound):
		auth.VerifyDummy(req.Password) // equalize timing for unknown/disabled users
	default:
		ip.Refund()
		pair.Refund()
		account.Refund()
		httpx.InternalError(w, r, err)
		return
	}
	if !ok {
		n := account.Failures()
		s.audit.LogUser(ctx, nil, audit.LoginFailed, req.Username, nil)
		if n%10 == 0 {
			slog.WarnContext(ctx, "login failure burst", "username", req.Username, "failures", n, "ip", httpx.IP(ctx))
			s.audit.LogUser(ctx, nil, audit.LoginBurst, req.Username, map[string]any{"failures": n})
		}
		httpx.Error(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	ip.Refund()
	account.Refund()
	pair.Reset()
	totp, keys, err := s.secondFactors(ctx, user)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	if totp || keys > 0 {
		httpx.JSON(w, http.StatusOK, map[string]any{"mfa_required": true, "token": s.pending.add(user.ID, "password"),
			"totp": totp, "webauthn": keys > 0})
		return
	}
	if s.startSession(w, r, user.ID, "password", false) {
		s.audit.LogUser(ctx, &user.ID, audit.LoginOK, user.Username, map[string]any{"method": "password"})
		httpx.OK(w)
	}
}

// startSession creates a session (replacing any presented in r), writing a
// 500 response on failure.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, userID int64, method string, mfa bool) bool {
	if _, err := s.sessions.Create(r.Context(), w, r, userID, method, mfa, httpx.IP(r.Context())); err != nil {
		httpx.InternalError(w, r, err)
		return false
	}
	return true
}

// rotateSessions ends all of the user's sessions and starts a new one for
// this client, after a change in privilege or credentials.
func (s *Server) rotateSessions(w http.ResponseWriter, r *http.Request, id *httpx.Identity, mfa bool) bool {
	if _, err := s.store.DeleteUserSessions(r.Context(), id.User.ID, nil); err != nil {
		httpx.InternalError(w, r, err)
		return false
	}
	return s.startSession(w, r, id.User.ID, id.Session.AuthMethod, mfa)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.sessions.Destroy(r.Context(), w, r)
	s.audit.Log(r.Context(), audit.Logout, "", nil)
	httpx.OK(w)
}

func (s *Server) handleLogoutAll(w http.ResponseWriter, r *http.Request) {
	id := httpx.IdentityFrom(r.Context())
	n, err := s.store.DeleteUserSessions(r.Context(), id.User.ID, nil)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	s.sessions.ClearCookie(w)
	s.audit.Log(r.Context(), audit.LogoutAll, "", map[string]any{"sessions": n})
	httpx.OK(w)
}

// freshLogin is how recent a login must be to add a sign-in method.
const freshLogin = 15 * time.Minute

// requireFreshLogin refuses to add a long-lived credential (TOTP, a passkey or
// security key) on an older session: a stolen session cookie must not be
// turned into a second factor or passkey that outlives it. Sessions are
// created at login, so their age is the time since the user authenticated.
func requireFreshLogin(w http.ResponseWriter, id *httpx.Identity) bool {
	if time.Since(id.Session.CreatedAt) <= freshLogin {
		return true
	}
	httpx.ErrorCode(w, http.StatusUnauthorized, "reauth_required",
		"for your security, log out and log in again to add a sign-in method")
	return false
}

// OIDC

// Invites
