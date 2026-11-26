package server

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"filebrowser/internal/audit"
	"filebrowser/internal/auth"
	"filebrowser/internal/httpx"
	"filebrowser/internal/store"
)

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
	req.Token = s.pendingToken(r, req.Token)
	pl := s.pending.attempt(req.Token)
	if pl == nil {
		httpx.ErrorCode(w, http.StatusUnauthorized, "login_expired", "login expired, start again")
		return
	}
	guess := s.totpBackoff.Slot(userKey("totp", pl.userID))
	if d := auth.Reserve(time.Now(), guess); d > 0 {
		retryAfter(w, d)
		return
	}
	user, err := s.store.UserByID(ctx, pl.userID)
	if err != nil || user.Disabled {
		guess.Refund()
		s.pending.done(req.Token)
		httpx.ErrorCode(w, http.StatusUnauthorized, "login_expired", "login expired, start again")
		return
	}
	if !s.verifyTOTP(r, user, req.Code) {
		s.audit.LogUser(ctx, &user.ID, audit.TOTPFailed, user.Username, nil)
		httpx.Error(w, http.StatusUnauthorized, "invalid code")
		return
	}
	guess.Reset()
	s.pending.done(req.Token)
	auth.ClearCookie(w, s.pendingCookie(), s.sessions.Secure, http.SameSiteStrictMode)
	if !s.startSession(w, r, user.ID, pl.method, true) {
		return
	}
	s.audit.LogUser(ctx, &user.ID, audit.LoginOK, user.Username, map[string]any{"method": pl.method, "totp": true})
	httpx.OK(w)
}

func (s *Server) handleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	id := httpx.IdentityFrom(r.Context())
	if id.User.TOTPEnabled() {
		httpx.Error(w, http.StatusConflict, "two-factor authentication is already enabled")
		return
	}
	if !requireFreshLogin(w, id) {
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
	if len(id.User.TOTPPending) == 0 {
		httpx.Error(w, http.StatusBadRequest, "start setup first")
		return
	}
	secret, err := s.keys.Decrypt(id.User.TOTPPending, totpAD(id.User.ID))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "start setup first")
		return
	}
	guess := s.totpBackoff.Slot(userKey("totp", id.User.ID))
	if d := auth.Reserve(time.Now(), guess); d > 0 {
		retryAfter(w, d)
		return
	}
	step := auth.CheckTOTP(secret, req.Code, time.Now())
	if step == 0 {
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
	pw, code := s.loginBackoff.Slot(userKey("pw", id.User.ID)), s.totpBackoff.Slot(userKey("totp", id.User.ID))
	if d := auth.Reserve(time.Now(), pw, code); d > 0 {
		retryAfter(w, d)
		return
	}
	if id.User.PasswordHash != "" && !auth.VerifyPassword(id.User.PasswordHash, req.Password) {
		code.Refund() // the code was not tried
		httpx.Error(w, http.StatusUnauthorized, "invalid password or code")
		return
	}
	pw.Refund()
	if !s.verifyTOTP(r, id.User, req.Code) {
		httpx.Error(w, http.StatusUnauthorized, "invalid password or code")
		return
	}
	code.Reset()
	if err := s.store.DisableTOTP(ctx, id.User.ID, requiresTOTP(id)); errors.Is(err, store.ErrLastLogin) {
		// A key removed since the check above: TOTP is now the last factor.
		httpx.Error(w, http.StatusForbidden, "two-factor authentication is required for your account: add a security key first")
		return
	} else if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	s.audit.Log(ctx, audit.TOTPDisabled, id.User.Username, nil)
	httpx.OK(w)
}
