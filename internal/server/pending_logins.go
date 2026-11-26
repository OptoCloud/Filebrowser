package server

import (
	"net/http"
	"sync"
	"time"

	"filebrowser/internal/auth"
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

func (s *Server) pendingCookie() string {
	return auth.CookieName(s.sessions.Secure, "pending", "fb_pending")
}

// pendingToken is the pending login a second-factor request is for: the one
// it names (after a password) or the one in the cookie (after a login
// provider).
func (s *Server) pendingToken(r *http.Request, named string) string {
	if named == "" {
		if c, err := r.Cookie(s.pendingCookie()); err == nil {
			return c.Value
		}
	}
	return named
}
