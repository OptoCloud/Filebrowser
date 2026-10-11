// Package server wires HTTP routes for the app and share origins.
package server

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"filebrowser/internal/audit"
	"filebrowser/internal/auth"
	"filebrowser/internal/config"
	"filebrowser/internal/files"
	"filebrowser/internal/httpx"
	"filebrowser/internal/media"
	"filebrowser/internal/shares"
	"filebrowser/internal/store"
	"filebrowser/internal/uploads"
)

// Deps are the services the server needs.
type Deps struct {
	Config config.Config
	Store  *store.Store
	Keys   *auth.Keys
	Files  *files.Service
	Media  *media.Service
	Web    fs.FS
	Now    func() time.Time
}

type Server struct {
	cfg      config.Config
	store    *store.Store
	keys     *auth.Keys
	sessions *auth.Sessions
	oidc     *auth.OIDC
	webauthn *auth.WebAuthn
	files    *files.Service
	media    *media.Service
	shares   *shares.Service
	uploads  *uploads.Uploads
	audit    *audit.Logger
	static   *static

	loginBackoff *auth.Backoff
	// accountBackoff slows distributed guessing against one account without
	// allowing a lockout: many free attempts and a short cap.
	accountBackoff *auth.Backoff
	totpBackoff    *auth.Backoff
	// shareBackoff limits password guesses per share across all clients.
	shareBackoff *auth.Backoff
	shareLimiter *auth.RateLimiter
	inviteLimit  *auth.RateLimiter
	passkeyLimit *auth.RateLimiter
	pending      *pendingLogins

	proxies      []netip.Prefix
	proxyWarning sync.Once

	appMux   *http.ServeMux
	shareMux *http.ServeMux
	// Routes records every registered app route for tests (deny-by-default checks).
	Routes []RouteInfo
}

type RouteInfo struct {
	Pattern string
	Level   Level
}

type Level int

const (
	Authed Level = iota
	Public
	Admin
)

type routeOpt func(*routeSpec)

type routeSpec struct {
	level           Level
	allowRestricted bool
	noBodyLimit     bool
}

func public() routeOpt      { return func(r *routeSpec) { r.level = Public } }
func adminOnly() routeOpt   { return func(r *routeSpec) { r.level = Admin } }
func restricted() routeOpt  { return func(r *routeSpec) { r.allowRestricted = true } }
func noBodyLimit() routeOpt { return func(r *routeSpec) { r.noBodyLimit = true } }

const maxBody = 1 << 20

func New(d Deps) (*Server, error) {
	cfg := d.Config
	secure := !cfg.Dev
	s := &Server{
		cfg:   cfg,
		store: d.Store,
		keys:  d.Keys,
		sessions: &auth.Sessions{
			Store: d.Store, Keys: d.Keys, Secure: secure, Now: d.Now,
			Idle: cfg.Session.IdleTimeout.Duration, Absolute: cfg.Session.AbsoluteTimeout.Duration,
		},
		oidc:  auth.NewOIDC(cfg.OIDC, cfg.AppOrigin, d.Keys, secure),
		files: d.Files,
		media: d.Media,
		audit: &audit.Logger{Store: d.Store},
		shares: &shares.Service{
			Store: d.Store, Keys: d.Keys, Files: d.Files, Secure: secure, ShareOrigin: cfg.ShareOrigin,
			DefaultExpiry: cfg.Shares.DefaultExpiry.Duration, MaxExpiry: cfg.Shares.MaxExpiry.Duration,
		},
		loginBackoff:   auth.NewBackoff(5, time.Second, 15*time.Minute, time.Hour),
		accountBackoff: auth.NewBackoff(20, time.Second, 30*time.Second, time.Hour),
		totpBackoff:    auth.NewBackoff(5, 2*time.Second, 15*time.Minute, time.Hour),
		shareBackoff:   auth.NewBackoff(10, time.Second, time.Minute, time.Hour),
		// Share token lookups: 60/min per IP with a burst of 120.
		shareLimiter: auth.NewRateLimiter(60, 120),
		inviteLimit:  auth.NewRateLimiter(10, 20),
		passkeyLimit: auth.NewRateLimiter(30, 30),
		pending:      newPendingLogins(),
		appMux:       http.NewServeMux(),
		shareMux:     http.NewServeMux(),
	}
	if s.shares.DefaultExpiry <= 0 || s.shares.DefaultExpiry > s.shares.MaxExpiry {
		s.shares.DefaultExpiry = s.shares.MaxExpiry
	}
	up, err := uploads.New(d.Files, s.shares, s.audit, cfg.AppOrigin, cfg.ShareOrigin, cfg.Uploads.MaxSize,
		func(ip string) bool { return s.shareLimiter.Allow(ip, time.Now()) })
	if err != nil {
		return nil, err
	}
	s.uploads = up
	if s.webauthn, err = auth.NewWebAuthn(cfg.AppOrigin); err != nil {
		return nil, err
	}
	if s.proxies, err = cfg.ProxyPrefixes(); err != nil {
		return nil, err
	}
	if s.static, err = newStatic(d.Web, cfg); err != nil {
		return nil, err
	}
	s.routes()
	return s, nil
}

// handle registers an app-origin route. Routes require a full session unless
// options say otherwise: deny by default.
func (s *Server) handle(pattern string, h http.HandlerFunc, opts ...routeOpt) {
	var spec routeSpec
	for _, o := range opts {
		o(&spec)
	}
	s.Routes = append(s.Routes, RouteInfo{Pattern: pattern, Level: spec.level})
	s.appMux.Handle(pattern, s.guard(spec, h))
}

func (s *Server) guard(spec routeSpec, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !spec.noBodyLimit {
			r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		}
		if spec.level == Public {
			h.ServeHTTP(w, r)
			return
		}
		id, ok := s.authenticate(w, r)
		if !ok {
			return
		}
		if s.isRestricted(id) && !spec.allowRestricted {
			httpx.ErrorCode(w, http.StatusForbidden, "totp_enrollment_required", "two-factor authentication must be set up first")
			return
		}
		if spec.level == Admin && !id.User.IsAdmin() {
			httpx.Error(w, http.StatusForbidden, "forbidden")
			return
		}
		h.ServeHTTP(w, r.WithContext(httpx.WithIdentity(r.Context(), id)))
	})
}

// authenticate resolves the session; it writes 401 and returns false if none.
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (*httpx.Identity, bool) {
	sess, user, err := s.sessions.Lookup(r.Context(), r)
	if err != nil {
		if !errors.Is(err, auth.ErrNoSession) {
			httpx.InternalError(w, r, err)
			return nil, false
		}
		httpx.ErrorCode(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
		return nil, false
	}
	access, err := files.AccessFor(r.Context(), s.store, user)
	if err != nil {
		httpx.InternalError(w, r, err)
		return nil, false
	}
	return &httpx.Identity{User: user, Session: sess, Access: access}, true
}

// requiresTOTP: a second factor is required for admins, and for anyone who
// can upload or create shares unless an admin exempted them.
func requiresTOTP(id *httpx.Identity) bool {
	return id.User.IsAdmin() || id.Access.CanWrite() && !id.User.MFAExempt
}

// isRestricted reports a session that may only enroll TOTP: the user needs
// a second factor but this session has not presented one.
func (s *Server) isRestricted(id *httpx.Identity) bool {
	return requiresTOTP(id) && !id.Session.MFA
}

// Handler returns the root handler, dispatching on Host.
func (s *Server) Handler() http.Handler {
	appHost, shareHost := s.cfg.AppHost(), s.cfg.ShareHost()
	app := s.csrf(s.cfg.AppOrigin, s.appMux)
	share := s.csrf(s.cfg.ShareOrigin, s.shareMux)
	return s.withIP(s.recoverer(s.logRequests(s.securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.ToLower(r.Host) {
		case appHost:
			app.ServeHTTP(w, r)
		case shareHost:
			share.ServeHTTP(w, r)
		default:
			http.Error(w, "unknown host", http.StatusMisdirectedRequest)
		}
	})))))
}

// Janitor runs periodic cleanup until ctx is done.
func (s *Server) Janitor(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		func() {
			defer func() {
				if v := recover(); v != nil {
					slog.Error("panic in cleanup", "err", v, "stack", string(debug.Stack()))
				}
			}()
			s.cleanup(ctx)
		}()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) cleanup(ctx context.Context) {
	now := time.Now()
	if n, err := s.store.DeleteSessionsBefore(ctx, now.Add(-s.cfg.Session.IdleTimeout.Duration), now); err != nil {
		slog.Error("session cleanup", "err", err)
	} else if n > 0 {
		slog.Info("expired sessions removed", "count", n)
	}
	if s.cfg.Trash.RetentionDays > 0 {
		cutoff := now.AddDate(0, 0, -s.cfg.Trash.RetentionDays)
		if n, err := s.files.PurgeOlderThan(ctx, cutoff); err != nil {
			slog.Error("trash purge", "err", err)
		} else if n > 0 {
			slog.Info("trash purged", "count", n)
		}
	}
	// Keep expired/revoked shares for 30 days for the owner's reference.
	if _, err := s.store.DeleteSharesExpiredBefore(ctx, now.AddDate(0, 0, -30)); err != nil {
		slog.Error("share cleanup", "err", err)
	}
	s.uploads.Cleanup(24 * time.Hour)
	s.pending.gc()
	s.webauthn.GC()
	if s.media != nil {
		s.media.Evict(ctx)
	}
}
