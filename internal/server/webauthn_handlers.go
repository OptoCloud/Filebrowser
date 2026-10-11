package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-webauthn/webauthn/webauthn"

	"filebrowser/internal/audit"
	"filebrowser/internal/auth"
	"filebrowser/internal/httpx"
	"filebrowser/internal/store"
)

// Security keys (YubiKey and other FIDO2 keys) and passkeys. A key is a
// second factor after a password or OIDC login, like TOTP, and satisfies the
// two-factor requirement for admins and writers. A passkey (a discoverable
// credential with user verification) logs in without a password.

// webauthnUser loads u's WebAuthn identity and registered keys.
func (s *Server) webauthnUser(ctx context.Context, u *store.User) (*auth.WebAuthnUser, error) {
	handle, err := s.store.WebAuthnHandle(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.WebAuthnCredentials(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	wu := &auth.WebAuthnUser{UserID: u.ID, Handle: handle, Name: u.Username}
	for _, r := range rows {
		var c webauthn.Credential
		if err := json.Unmarshal(r.Credential, &c); err != nil {
			return nil, err
		}
		wu.Creds = append(wu.Creds, c)
	}
	return wu, nil
}

// saveCredentialUse stores a key's new sign count and flags after a login.
func (s *Server) saveCredentialUse(ctx context.Context, c *webauthn.Credential) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return s.store.UseWebAuthnCredential(ctx, c.ID, raw, time.Now())
}

// secondFactors reports which second factors the user has set up.
func (s *Server) secondFactors(ctx context.Context, u *store.User) (totp bool, keys int, err error) {
	rows, err := s.store.WebAuthnCredentials(ctx, u.ID)
	return u.TOTPEnabled(), len(rows), err
}

// methods lists the second factors for the login page.
func methods(totp bool, keys int) []string {
	var m []string
	if totp {
		m = append(m, "totp")
	}
	if keys > 0 {
		m = append(m, "webauthn")
	}
	return m
}

type ceremonyResponse struct {
	Ceremony string `json:"ceremony"`
	Options  any    `json:"options"`
}

// Second factor

func (s *Server) handleLoginWebAuthnBegin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	pl := s.pending.peek(req.Token)
	if pl == nil {
		httpx.ErrorCode(w, http.StatusUnauthorized, "login_expired", "login expired, start again")
		return
	}
	user, err := s.store.UserByID(ctx, pl.userID)
	if err != nil || user.Disabled {
		httpx.ErrorCode(w, http.StatusUnauthorized, "login_expired", "login expired, start again")
		return
	}
	wu, err := s.webauthnUser(ctx, user)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	if len(wu.Creds) == 0 {
		httpx.Error(w, http.StatusBadRequest, "no security key is registered")
		return
	}
	tok, options, err := s.webauthn.BeginLogin(wu, req.Token)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, ceremonyResponse{Ceremony: tok, Options: options})
}

func (s *Server) handleLoginWebAuthnFinish(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Ceremony   string          `json:"ceremony"`
		Credential json.RawMessage `json:"credential"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	c, err := s.webauthn.Take(req.Ceremony, auth.CeremonyLogin)
	var pl *pendingLogin
	if err == nil {
		pl = s.pending.attempt(c.Pending)
	}
	if pl == nil || pl.userID != c.UserID {
		httpx.ErrorCode(w, http.StatusUnauthorized, "login_expired", "login expired, start again")
		return
	}
	// One budget for all second-factor guesses, whichever method.
	key := userKey("totp", pl.userID)
	if d := s.totpBackoff.Wait(key, time.Now()); d > 0 {
		retryAfter(w, d)
		return
	}
	user, err := s.store.UserByID(ctx, pl.userID)
	if err != nil || user.Disabled {
		s.pending.done(c.Pending)
		httpx.ErrorCode(w, http.StatusUnauthorized, "login_expired", "login expired, start again")
		return
	}
	wu, err := s.webauthnUser(ctx, user)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	cred, err := s.webauthn.ValidateLogin(wu, c, req.Credential)
	if err != nil {
		s.totpBackoff.Fail(key, time.Now())
		s.audit.LogUser(ctx, &user.ID, audit.WebAuthnFailed, user.Username, map[string]any{"error": err.Error()})
		httpx.Error(w, http.StatusUnauthorized, "security key not accepted")
		return
	}
	if err := s.saveCredentialUse(ctx, cred); err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	s.totpBackoff.Reset(key)
	s.pending.done(c.Pending)
	if s.startSession(w, r, user.ID, pl.method, true) {
		s.audit.LogUser(ctx, &user.ID, audit.LoginOK, user.Username, map[string]any{"method": pl.method, "webauthn": true})
		httpx.OK(w)
	}
}

// Passkeys

func (s *Server) handlePasskeyBegin(w http.ResponseWriter, r *http.Request) {
	if !s.passkeyLimit.Allow(httpx.LimitKey(r.Context()), time.Now()) {
		retryAfter(w, time.Minute)
		return
	}
	tok, options, err := s.webauthn.BeginPasskey()
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, ceremonyResponse{Ceremony: tok, Options: options})
}

func (s *Server) handlePasskeyFinish(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Ceremony   string          `json:"ceremony"`
		Credential json.RawMessage `json:"credential"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	ipKey := "ip:" + httpx.LimitKey(ctx)
	if d := s.loginBackoff.Wait(ipKey, time.Now()); d > 0 {
		retryAfter(w, d)
		return
	}
	c, err := s.webauthn.Take(req.Ceremony, auth.CeremonyPasskey)
	if err != nil {
		httpx.ErrorCode(w, http.StatusUnauthorized, "login_expired", "login expired, start again")
		return
	}
	var user *store.User
	wu, cred, err := s.webauthn.ValidatePasskey(c, req.Credential, func(handle []byte) (*auth.WebAuthnUser, error) {
		u, err := s.store.UserByWebAuthnHandle(ctx, handle)
		if err != nil {
			return nil, err
		}
		if u.Disabled {
			return nil, errors.New("account disabled")
		}
		user = u
		return s.webauthnUser(ctx, u)
	})
	if err != nil || wu == nil || user == nil {
		s.loginBackoff.Fail(ipKey, time.Now())
		s.audit.LogUser(ctx, nil, audit.WebAuthnFailed, "", map[string]any{"passkey": true, "error": errString(err)})
		httpx.Error(w, http.StatusUnauthorized, "passkey not accepted")
		return
	}
	if err := s.saveCredentialUse(ctx, cred); err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	// The authenticator verified the user (PIN or biometric) as well as
	// proving possession: two factors.
	if s.startSession(w, r, user.ID, "passkey", true) {
		s.audit.LogUser(ctx, &user.ID, audit.LoginOK, user.Username, map[string]any{"method": "passkey"})
		httpx.OK(w)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// Managing keys

type webauthnView struct {
	ID string `json:"id"`
	*store.WebAuthnCredential
}

func (s *Server) handleWebAuthnList(w http.ResponseWriter, r *http.Request) {
	id := httpx.IdentityFrom(r.Context())
	rows, err := s.store.WebAuthnCredentials(r.Context(), id.User.ID)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	out := make([]webauthnView, 0, len(rows))
	for _, c := range rows {
		out = append(out, webauthnView{ID: base64.RawURLEncoding.EncodeToString(c.ID), WebAuthnCredential: c})
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (s *Server) handleWebAuthnRegisterBegin(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if !httpx.Decode(w, r, &req) {
		return
	}
	id := httpx.IdentityFrom(r.Context())
	wu, err := s.webauthnUser(r.Context(), id.User)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	tok, options, err := s.webauthn.BeginRegistration(wu)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, ceremonyResponse{Ceremony: tok, Options: options})
}

// validKeyName accepts 1-64 printable characters.
func validKeyName(n string) bool {
	if n == "" || utf8.RuneCountInString(n) > 64 || !utf8.ValidString(n) {
		return false
	}
	return !strings.ContainsFunc(n, func(r rune) bool { return !unicode.IsPrint(r) })
}

func (s *Server) handleWebAuthnRegisterFinish(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Ceremony   string          `json:"ceremony"`
		Name       string          `json:"name"`
		Credential json.RawMessage `json:"credential"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	id := httpx.IdentityFrom(ctx)
	req.Name = strings.TrimSpace(req.Name)
	if !validKeyName(req.Name) {
		httpx.Error(w, http.StatusBadRequest, "name the key (1 to 64 characters)")
		return
	}
	c, err := s.webauthn.Take(req.Ceremony, auth.CeremonyRegister)
	if err != nil || c.UserID != id.User.ID {
		httpx.Error(w, http.StatusBadRequest, "registration expired, start again")
		return
	}
	wu, err := s.webauthnUser(ctx, id.User)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	cred, err := s.webauthn.CreateCredential(wu, c, req.Credential)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "security key registration failed")
		return
	}
	raw, err := json.Marshal(cred)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	row := &store.WebAuthnCredential{ID: cred.ID, UserID: id.User.ID, Name: req.Name, Credential: raw, Passkey: auth.IsPasskey(cred)}
	if err := s.store.AddWebAuthnCredential(ctx, row); errors.Is(err, store.ErrConflict) {
		httpx.Error(w, http.StatusConflict, "this key is already registered")
		return
	} else if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	s.audit.Log(ctx, audit.WebAuthnAdded, id.User.Username, map[string]any{"name": req.Name, "passkey": row.Passkey})
	// Enrolling a second factor from a session without one: like enabling
	// TOTP, rotate the session (now with MFA) and drop all others.
	if !id.Session.MFA && !s.rotateSessions(w, r, id, true) {
		return
	}
	httpx.OK(w)
}

func (s *Server) handleWebAuthnDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := httpx.IdentityFrom(ctx)
	credID, err := base64.RawURLEncoding.DecodeString(r.PathValue("id"))
	if err != nil || len(credID) == 0 || len(credID) > 1023 {
		httpx.Error(w, http.StatusNotFound, "not found")
		return
	}
	err = s.store.DeleteWebAuthnCredential(ctx, id.User.ID, credID, requiresTOTP(id))
	switch {
	case errors.Is(err, store.ErrLastLogin):
		httpx.ErrorCode(w, http.StatusConflict, "last_second_factor",
			"your account requires two-factor authentication: add another key or set up an authenticator app first")
		return
	case err != nil:
		httpx.FileError(w, r, err)
		return
	}
	s.audit.Log(ctx, audit.WebAuthnRemoved, id.User.Username, nil)
	httpx.OK(w)
}
