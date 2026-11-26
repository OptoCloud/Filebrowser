package shares

import (
	"context"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"

	"filebrowser/internal/auth"
	"filebrowser/internal/store"
)

// The share origin's cookies are the server's word, signed and dated, about
// what a browser has done with a share: "<expiry>.<signature>", where the
// signature is over the subject and the expiry under a purpose of its own.
const (
	purposeUnlock  = "share-unlock"
	purposeCounted = "share-counted"
	// unlockWindow is how long a share stays unlocked after its password.
	unlockWindow = 12 * time.Hour
)

func (s *Service) sessionCookieName(id int64) string { return s.cookieName("fbd", id) }

// Session reports whether the browser may use sh: always, unless sh is a
// single-session link, which admits only the first browser to open it. With
// w set, a link nobody has opened yet is claimed for this browser.
func (s *Service) Session(ctx context.Context, w http.ResponseWriter, r *http.Request, sh *store.Share) (bool, error) {
	if !sh.SingleSession {
		return true, nil
	}
	if c, err := r.Cookie(s.sessionCookieName(sh.ID)); err == nil && sh.SessionHash != nil {
		return auth.ConstantTimeEqual(s.Keys.HashToken(c.Value), sh.SessionHash), nil
	}
	if sh.SessionHash != nil || w == nil {
		return false, nil
	}
	nonce := auth.NewToken(18)
	ok, err := s.Store.ClaimShareSession(ctx, sh.ID, s.Keys.HashToken(nonce))
	if err != nil || !ok {
		return false, err
	}
	auth.SetCookie(w, auth.Cookie{Name: s.sessionCookieName(sh.ID), Value: nonce, Expires: sh.ExpiresAt,
		SameSite: http.SameSiteStrictMode}, s.Secure)
	return true, nil
}

func (s *Service) unlockCookieName(id int64) string { return s.cookieName("fbs", id) }

// unlockSubject is what an unlock cookie vouches for: the share with the
// password it had, so that a changed password asks again.
func unlockSubject(sh *store.Share) string {
	return strconv.FormatInt(sh.ID, 10) + "|" + sh.PasswordHash
}

// Unlocked reports whether r carries a valid unlock cookie (or no password is set).
func (s *Service) Unlocked(r *http.Request, sh *store.Share) bool {
	if sh.PasswordHash == "" {
		return true
	}
	c, err := r.Cookie(s.unlockCookieName(sh.ID))
	return err == nil && s.validUntil(purposeUnlock, unlockSubject(sh), c.Value)
}

// Unlock verifies the password and sets the unlock cookie.
func (s *Service) Unlock(w http.ResponseWriter, sh *store.Share, password string) bool {
	if sh.PasswordHash == "" {
		return true
	}
	if !auth.VerifyPassword(sh.PasswordHash, password) {
		return false
	}
	s.setUntil(w, s.unlockCookieName(sh.ID), purposeUnlock, unlockSubject(sh), time.Now().Add(unlockWindow), sh)
	return true
}

func (s *Service) countedCookieName(id int64) string { return s.cookieName("fbc", id) }

// countedWindow is how long a browser that used a download of a limited
// link may keep using it (seeking in a video, resuming, listing again)
// without using another. The cookie is not tied to one browser, so the
// window is what bounds a copy of it handed to someone else.
const countedWindow = 24 * time.Hour

// Counted reports whether this browser has used a download of sh recently
// enough to go on without using another.
func (s *Service) Counted(r *http.Request, sh *store.Share) bool {
	c, err := r.Cookie(s.countedCookieName(sh.ID))
	return err == nil && s.validUntil(purposeCounted, strconv.FormatInt(sh.ID, 10), c.Value)
}

// MarkCounted records that this browser has used a download of sh.
func (s *Service) MarkCounted(w http.ResponseWriter, sh *store.Share) {
	s.setUntil(w, s.countedCookieName(sh.ID), purposeCounted, strconv.FormatInt(sh.ID, 10), time.Now().Add(countedWindow), sh)
}

func (s *Service) cookieName(tag string, id int64) string {
	n := tag + strconv.FormatInt(id, 10)
	return auth.CookieName(s.Secure, n, n)
}

func untilMsg(subject string, exp int64) []byte {
	return []byte(subject + "|" + strconv.FormatInt(exp, 10))
}

// setUntil sets a cookie vouching for subject until the given time, or
// until the share expires if that is sooner.
func (s *Service) setUntil(w http.ResponseWriter, name, purpose, subject string, until time.Time, sh *store.Share) {
	exp := min(until.Unix(), sh.ExpiresAt.Unix())
	sig := s.Keys.Sign(purpose, untilMsg(subject, exp))
	auth.SetCookie(w, auth.Cookie{
		Name:    name,
		Value:   strconv.FormatInt(exp, 10) + "." + base64.RawURLEncoding.EncodeToString(sig),
		Expires: time.Unix(exp, 0), SameSite: http.SameSiteLaxMode,
	}, s.Secure)
}

// validUntil checks a value made by setUntil for subject.
func (s *Service) validUntil(purpose, subject, value string) bool {
	expStr, sigStr, ok := strings.Cut(value, ".")
	if !ok {
		return false
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigStr)
	return err == nil && s.Keys.Verify(purpose, untilMsg(subject, exp), sig)
}
