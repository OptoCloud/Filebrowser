package server

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"filebrowser/internal/audit"
	"filebrowser/internal/auth"
	"filebrowser/internal/files"
	"filebrowser/internal/httpx"
	"filebrowser/internal/store"
)

// pendingLogins holds password-verified logins awaiting a TOTP code.
type pendingLogins struct {
	mu sync.Mutex
	m  map[string]*pendingLogin
}

type pendingLogin struct {
	userID   int64
	method   string
	expires  time.Time
	attempts int
}

func newPendingLogins() *pendingLogins { return &pendingLogins{m: map[string]*pendingLogin{}} }

func (p *pendingLogins) add(userID int64, method string) string {
	tok := auth.NewToken(24)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.m[tok] = &pendingLogin{userID: userID, method: method, expires: time.Now().Add(5 * time.Minute)}
	return tok
}

// peek returns the pending login if still valid, without counting an attempt.
func (p *pendingLogins) peek(tok string) *pendingLogin {
	p.mu.Lock()
	defer p.mu.Unlock()
	pl := p.m[tok]
	if pl == nil || time.Now().After(pl.expires) || pl.attempts >= 5 {
		return nil
	}
	return pl
}

// attempt returns the pending login if still valid, counting an attempt.
func (p *pendingLogins) attempt(tok string) *pendingLogin {
	p.mu.Lock()
	defer p.mu.Unlock()
	pl := p.m[tok]
	if pl == nil {
		return nil
	}
	if time.Now().After(pl.expires) || pl.attempts >= 5 {
		delete(p.m, tok)
		return nil
	}
	pl.attempts++
	return pl
}

func (p *pendingLogins) done(tok string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.m, tok)
}

func (p *pendingLogins) gc() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, v := range p.m {
		if time.Now().After(v.expires) {
			delete(p.m, k)
		}
	}
}

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
	// Hard backoff per IP and per IP+account. The account-wide limit is soft
	// (short cap) so that failures from elsewhere cannot lock the owner out.
	ipKey := "ip:" + httpx.LimitKey(ctx)
	pairKey := "ipuser:" + httpx.LimitKey(ctx) + "|" + strings.ToLower(req.Username)
	userKey := "user:" + strings.ToLower(req.Username)
	if d := max(s.loginBackoff.Wait(ipKey, now), s.loginBackoff.Wait(pairKey, now), s.accountBackoff.Wait(userKey, now)); d > 0 {
		retryAfter(w, d)
		return
	}
	if len(req.Username) > 64 || len(req.Password) > auth.MaxPasswordLen {
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
		httpx.InternalError(w, r, err)
		return
	}
	if !ok {
		s.loginBackoff.Fail(ipKey, now)
		s.loginBackoff.Fail(pairKey, now)
		n := s.accountBackoff.Fail(userKey, now)
		s.audit.LogUser(ctx, nil, audit.LoginFailed, req.Username, nil)
		if n%10 == 0 {
			slog.WarnContext(ctx, "login failure burst", "username", req.Username, "failures", n, "ip", httpx.IP(ctx))
			s.audit.LogUser(ctx, nil, audit.LoginBurst, req.Username, map[string]any{"failures": n})
		}
		httpx.Error(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	s.loginBackoff.Reset(pairKey)
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

// verifyTOTP checks a code against the user's secret with replay protection.
func (s *Server) verifyTOTP(r *http.Request, user *store.User, code string) bool {
	if !user.TOTPEnabled() {
		return false
	}
	secret, err := s.keys.Decrypt(user.TOTPSecret, totpAD(user.ID))
	if err != nil {
		return false
	}
	step := auth.CheckTOTP(secret, code, time.Now())
	if step == 0 {
		return false
	}
	ok, err := s.store.ConsumeTOTPStep(r.Context(), user.ID, step)
	return err == nil && ok
}

func totpAD(userID int64) []byte { return []byte("totp:" + strconv.FormatInt(userID, 10)) }

func (s *Server) handleLoginTOTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
		Code  string `json:"code"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	pl := s.pending.attempt(req.Token)
	if pl == nil {
		httpx.ErrorCode(w, http.StatusUnauthorized, "login_expired", "login expired, start again")
		return
	}
	key := userKey("totp", pl.userID)
	if d := s.totpBackoff.Wait(key, time.Now()); d > 0 {
		retryAfter(w, d)
		return
	}
	user, err := s.store.UserByID(ctx, pl.userID)
	if err != nil || user.Disabled {
		s.pending.done(req.Token)
		httpx.ErrorCode(w, http.StatusUnauthorized, "login_expired", "login expired, start again")
		return
	}
	if !s.verifyTOTP(r, user, req.Code) {
		s.totpBackoff.Fail(key, time.Now())
		s.audit.LogUser(ctx, &user.ID, audit.TOTPFailed, user.Username, nil)
		httpx.Error(w, http.StatusUnauthorized, "invalid code")
		return
	}
	s.totpBackoff.Reset(key)
	s.pending.done(req.Token)
	if !s.startSession(w, r, user.ID, pl.method, true) {
		return
	}
	s.audit.LogUser(ctx, &user.ID, audit.LoginOK, user.Username, map[string]any{"method": pl.method, "totp": true})
	httpx.OK(w)
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
		"has_password":   id.User.PasswordHash != "",
		"oidc":           idents,
		"providers":      s.oidc.Providers(),
		"share_origin":   s.cfg.ShareOrigin,
	}
	if id.User.QuotaBytes > 0 {
		resp["usage_bytes"] = s.files.Usage(r.Context(), id.User.ID, id.Access)
	}
	httpx.JSON(w, http.StatusOK, resp)
}

func (s *Server) handleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	id := httpx.IdentityFrom(r.Context())
	if id.User.TOTPEnabled() {
		httpx.Error(w, http.StatusConflict, "two-factor authentication is already enabled")
		return
	}
	secret := auth.NewTOTPSecret()
	enc, err := s.keys.Encrypt(secret, totpAD(id.User.ID))
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	if err := s.store.SetTOTPPending(r.Context(), id.User.ID, enc); err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	issuer := "filebrowser"
	if u, err := url.Parse(s.cfg.AppOrigin); err == nil {
		issuer = u.Hostname()
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"secret": auth.TOTPSecretString(secret),
		"url":    auth.TOTPURL(issuer, id.User.Username, secret),
	})
}

func (s *Server) handleTOTPEnable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	id := httpx.IdentityFrom(ctx)
	if id.User.TOTPEnabled() {
		httpx.Error(w, http.StatusConflict, "two-factor authentication is already enabled")
		return
	}
	key := userKey("totp", id.User.ID)
	if d := s.totpBackoff.Wait(key, time.Now()); d > 0 {
		retryAfter(w, d)
		return
	}
	if len(id.User.TOTPPending) == 0 {
		httpx.Error(w, http.StatusBadRequest, "start setup first")
		return
	}
	secret, err := s.keys.Decrypt(id.User.TOTPPending, totpAD(id.User.ID))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "start setup first")
		return
	}
	step := auth.CheckTOTP(secret, req.Code, time.Now())
	if step == 0 {
		s.totpBackoff.Fail(key, time.Now())
		httpx.Error(w, http.StatusBadRequest, "invalid code")
		return
	}
	if err := s.store.EnableTOTP(ctx, id.User.ID, id.User.TOTPPending, step); err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	// Privilege change: rotate the session and drop all others.
	if !s.rotateSessions(w, r, id, true) {
		return
	}
	s.audit.Log(ctx, audit.TOTPEnabled, id.User.Username, nil)
	httpx.OK(w)
}

func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	id := httpx.IdentityFrom(ctx)
	keys, err := s.store.WebAuthnCredentials(ctx, id.User.ID)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	if requiresTOTP(id) && len(keys) == 0 {
		httpx.Error(w, http.StatusForbidden, "two-factor authentication is required for your account: add a security key first")
		return
	}
	if !id.User.TOTPEnabled() {
		httpx.OK(w)
		return
	}
	// Same limits as password change and TOTP entry: a session alone must not
	// allow unthrottled guessing of the password or the code.
	pwKey, totpKey := userKey("pw", id.User.ID), userKey("totp", id.User.ID)
	if d := max(s.loginBackoff.Wait(pwKey, time.Now()), s.totpBackoff.Wait(totpKey, time.Now())); d > 0 {
		retryAfter(w, d)
		return
	}
	if id.User.PasswordHash != "" && !auth.VerifyPassword(id.User.PasswordHash, req.Password) {
		s.loginBackoff.Fail(pwKey, time.Now())
		httpx.Error(w, http.StatusUnauthorized, "invalid password or code")
		return
	}
	if !s.verifyTOTP(r, id.User, req.Code) {
		s.totpBackoff.Fail(totpKey, time.Now())
		httpx.Error(w, http.StatusUnauthorized, "invalid password or code")
		return
	}
	s.totpBackoff.Reset(totpKey)
	if err := s.store.DisableTOTP(ctx, id.User.ID); err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	s.audit.Log(ctx, audit.TOTPDisabled, id.User.Username, nil)
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
	key := userKey("pw", id.User.ID)
	if d := s.loginBackoff.Wait(key, time.Now()); d > 0 {
		retryAfter(w, d)
		return
	}
	if id.User.PasswordHash != "" && !auth.VerifyPassword(id.User.PasswordHash, req.Current) {
		s.loginBackoff.Fail(key, time.Now())
		httpx.Error(w, http.StatusUnauthorized, "current password is wrong")
		return
	}
	// A first password (OIDC-only account) is a new long-lived credential:
	// require a second factor or a fresh login, not just a session.
	if id.User.PasswordHash == "" {
		switch {
		case id.User.TOTPEnabled():
			if !s.verifyTOTP(r, id.User, req.Code) {
				s.loginBackoff.Fail(key, time.Now())
				httpx.Error(w, http.StatusUnauthorized, "invalid authenticator code")
				return
			}
		case time.Since(id.Session.CreatedAt) > 10*time.Minute:
			httpx.ErrorCode(w, http.StatusUnauthorized, "reauth_required", "log in again to set a password")
			return
		}
	}
	if err := auth.ValidatePassword(req.New); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	hash, err := auth.HashPassword(req.New)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	if err := s.store.SetPassword(ctx, id.User.ID, hash); err != nil {
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

// OIDC

func (s *Server) handleOIDCStart(w http.ResponseWriter, r *http.Request) {
	var link int64
	if r.URL.Query().Get("link") == "1" {
		id, ok := s.authenticate(w, r)
		if !ok {
			return
		}
		if s.isRestricted(id) {
			httpx.Error(w, http.StatusForbidden, "forbidden")
			return
		}
		link = id.User.ID
	}
	u, err := s.oidc.Start(r.Context(), w, r.URL.Query().Get("provider"), link)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "login provider unavailable")
		return
	}
	http.Redirect(w, r, u, http.StatusFound) // #nosec G710 nosemgrep -- u is the provider authorization URL built from server config
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
	trust := s.oidc.TrustMFA(ident.Provider)
	totp, keys, err := s.secondFactors(ctx, user)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	if !trust && (totp || keys > 0) {
		tok := s.pending.add(user.ID, "oidc")
		http.Redirect(w, r, "/login?mfa="+url.QueryEscape(tok)+"&methods="+strings.Join(methods(totp, keys), ","), http.StatusFound)
		return
	}
	if !s.startSession(w, r, user.ID, "oidc", trust) {
		return
	}
	s.audit.LogUser(ctx, &user.ID, audit.OIDCLogin, target, nil)
	http.Redirect(w, r, "/", http.StatusFound)
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

// Invites

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
	if err := auth.ValidatePassword(req.Password); err != nil {
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
