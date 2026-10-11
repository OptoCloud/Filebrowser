package auth

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"filebrowser/internal/store"
)

var ErrNoSession = errors.New("no valid session")

type Sessions struct {
	Store    *store.Store
	Keys     *Keys
	Idle     time.Duration
	Absolute time.Duration
	// Secure enables the __Host- prefix and the Secure attribute (off only in dev).
	Secure bool
	Now    func() time.Time
}

func (m *Sessions) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Sessions) CookieName() string {
	if m.Secure {
		return "__Host-session"
	}
	return "fb_session"
}

// Create starts a new session, replacing (rotating) any session presented in r.
func (m *Sessions) Create(ctx context.Context, w http.ResponseWriter, r *http.Request, userID int64, method string, mfa bool, ip string) (*store.Session, error) {
	if c, err := r.Cookie(m.CookieName()); err == nil {
		_ = m.Store.DeleteSession(ctx, m.Keys.HashToken(c.Value))
	}
	token := NewToken(32)
	now := m.now()
	ua := r.UserAgent()
	if len(ua) > 256 {
		ua = ua[:256]
	}
	sess := &store.Session{
		IDHash:     m.Keys.HashToken(token),
		UserID:     userID,
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  now.Add(m.Absolute),
		IP:         ip,
		UserAgent:  ua,
		AuthMethod: method,
		MFA:        mfa,
	}
	if err := m.Store.CreateSession(ctx, sess); err != nil {
		return nil, err
	}
	SetCookie(w, Cookie{Name: m.CookieName(), Value: token, Expires: sess.ExpiresAt, SameSite: http.SameSiteStrictMode}, m.Secure)
	return sess, nil
}

// Lookup validates the session cookie and returns the session and its user.
func (m *Sessions) Lookup(ctx context.Context, r *http.Request) (*store.Session, *store.User, error) {
	c, err := r.Cookie(m.CookieName())
	if err != nil || len(c.Value) < 40 || len(c.Value) > 64 {
		return nil, nil, ErrNoSession
	}
	hash := m.Keys.HashToken(c.Value)
	sess, err := m.Store.SessionByHash(ctx, hash)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, ErrNoSession
	} else if err != nil {
		return nil, nil, err
	}
	now := m.now()
	if now.After(sess.ExpiresAt) || now.Sub(sess.LastSeenAt) > m.Idle {
		_ = m.Store.DeleteSession(ctx, hash)
		return nil, nil, ErrNoSession
	}
	user, err := m.Store.UserByID(ctx, sess.UserID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, ErrNoSession
	} else if err != nil {
		return nil, nil, err
	}
	if user.Disabled {
		_ = m.Store.DeleteSession(ctx, hash)
		return nil, nil, ErrNoSession
	}
	if now.Sub(sess.LastSeenAt) > time.Minute {
		_ = m.Store.TouchSession(ctx, hash, now)
		sess.LastSeenAt = now
	}
	return sess, user, nil
}

// Destroy deletes the session in r and clears the cookie.
func (m *Sessions) Destroy(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(m.CookieName()); err == nil {
		_ = m.Store.DeleteSession(ctx, m.Keys.HashToken(c.Value))
	}
	m.ClearCookie(w)
}

func (m *Sessions) ClearCookie(w http.ResponseWriter) {
	ClearCookie(w, m.CookieName(), m.Secure, http.SameSiteStrictMode)
}

// PublicID is the identifier shown to users for a session (hex of the hash).
func PublicID(hash []byte) string { return hex.EncodeToString(hash) }

func ParsePublicID(id string) ([]byte, error) {
	b, err := hex.DecodeString(id)
	if err != nil || len(b) != 32 {
		return nil, errors.New("invalid session id")
	}
	return b, nil
}
