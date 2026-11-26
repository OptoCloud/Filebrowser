// Package server wires HTTP routes for the app and share origins.
package server

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"filebrowser/internal/audit"
	"filebrowser/internal/auth"
	"filebrowser/internal/config"
	"filebrowser/internal/files"
	"filebrowser/internal/httpx"
	"filebrowser/internal/media"
	"filebrowser/internal/metrics"
	"filebrowser/internal/r2"
	"filebrowser/internal/shares"
	"filebrowser/internal/store"
	"filebrowser/internal/uploads"
	"filebrowser/internal/webhooks"
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
	dropLimiter  *auth.RateLimiter
	tokenLimiter *auth.RateLimiter
	// reportLimiter bounds the policy violation reports logged per client.
	reportLimiter *auth.RateLimiter
	hooks         *webhooks.Dispatcher
	passwords     *auth.PasswordPolicy
	sizes         sizeCache
	// Metrics counts what the server does (see MetricsListen).
	Metrics *metrics.Registry
	// r2 is the upload bucket, or nil when none is configured.
	r2           *uploads.R2
	inviteLimit  *auth.RateLimiter
	passkeyLimit *auth.RateLimiter
	// ceremonyLimit caps the security-key ceremonies one user can start: they
	// share a bounded pool, which one account must not be able to fill.
	ceremonyLimit *auth.RateLimiter
	pending       *pendingLogins

	// proxies are the trusted proxies: staticProxies from the configuration
	// and, if it names them, Cloudflare's ranges as of cloudflareAt.
	proxies       atomic.Pointer[[]netip.Prefix]
	staticProxies []netip.Prefix
	cloudflareAt  time.Time
	proxyWarning  sync.Once

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
	// passwordChange marks what a user who must first replace a password
	// chosen for them may still do (see User.MustChangePassword).
	passwordChange bool
	// tokenCap is the capability an API token needs to use the route; with
	// none, the route does not take API tokens at all.
	tokenCap string
}

func public() routeOpt         { return func(r *routeSpec) { r.level = Public } }
func adminOnly() routeOpt      { return func(r *routeSpec) { r.level = Admin } }
func restricted() routeOpt     { return func(r *routeSpec) { r.allowRestricted = true } }
func passwordChange() routeOpt { return func(r *routeSpec) { r.passwordChange = true } }
func noBodyLimit() routeOpt    { return func(r *routeSpec) { r.noBodyLimit = true } }

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
			DropMaxBytes: cfg.Shares.DropMaxBytes, DropMaxFiles: cfg.Shares.DropMaxFiles, UploadMaxSize: cfg.Uploads.MaxSize,
		},
		loginBackoff:   loginFailures.new(),
		accountBackoff: accountFailures.new(),
		totpBackoff:    totpFailures.new(),
		shareBackoff:   sharePasswordFailures.new(),
		shareLimiter:   shareLookups.limiter(),
		dropLimiter:    dropUploads.limiter(),
		tokenLimiter:   badTokens.limiter(),
		reportLimiter:  policyReports.limiter(),
		inviteLimit:    inviteLookups.limiter(),
		passkeyLimit:   passkeyLogins.limiter(),
		ceremonyLimit:  ceremonies.limiter(),
		pending:        newPendingLogins(),
		appMux:         http.NewServeMux(),
		shareMux:       http.NewServeMux(),
	}
	if s.shares.DefaultExpiry <= 0 || s.shares.DefaultExpiry > s.shares.MaxExpiry {
		s.shares.DefaultExpiry = s.shares.MaxExpiry
	}
	up, err := uploads.New(d.Files, s.shares, s.audit, cfg.AppOrigin, cfg.ShareOrigin, cfg.Uploads.MaxSize,
		func(ip string) bool { return s.shareLimiter.Allow(ip, time.Now()) },
		func(ip string) bool { return s.dropLimiter.Allow(ip, time.Now()) })
	if err != nil {
		return nil, err
	}
	s.uploads = up
	if cfg.R2.Enabled() {
		client, err := r2.New(cfg.R2.Endpoint, cfg.R2.Bucket, r2.Credentials{
			AccessKeyID: cfg.R2.AccessKeyID, SecretAccessKey: cfg.R2.SecretAccessKey, Region: cfg.R2.Region})
		if err != nil {
			return nil, err
		}
		s.r2 = up.EnableR2(client, cfg.R2)
	}
	if s.webauthn, err = auth.NewWebAuthn(cfg.AppOrigin); err != nil {
		return nil, err
	}
	if s.staticProxies, err = cfg.ProxyPrefixes(); err != nil {
		return nil, err
	}
	s.setProxies(nil)
	if cfg.TrustsCloudflare() {
		s.loadCloudflareRanges()
	}
	if s.static, err = newStatic(d.Web, cfg); err != nil {
		return nil, err
	}
	s.static.shareCard = s.shareCard
	s.passwords = &auth.PasswordPolicy{Pwned: cfg.CheckPwnedPasswords}
	s.Metrics = &metrics.Registry{}
	s.Metrics.Describe("filebrowser_http_requests_total", "Requests answered, by origin and status class.")
	s.Metrics.Describe("filebrowser_http_response_bytes_total", "Bytes sent in responses.")
	s.Metrics.Describe("filebrowser_uploads_total", "Files received, by the app (also API tokens) or through drop links.")
	s.Metrics.Describe("filebrowser_upload_bytes_total", "Bytes of the files received.")
	s.Metrics.Describe("filebrowser_logins_total", "Logins and failed login attempts (any factor).")
	s.Metrics.Describe("filebrowser_share_downloads_total", "Downloads counted through share links.")
	s.Metrics.Describe("filebrowser_shares_created_total", "Share links made.")
	s.hooks = webhooks.New(d.Store, d.Keys)
	s.audit.Notify = s.notifyHooks
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
		// A request that carries an API token is that token's and nothing
		// else: a session cookie beside it is not looked at.
		if raw, ok := bearer(r); ok {
			if id, ok := s.authenticateToken(w, r, raw, spec); ok {
				h.ServeHTTP(w, r.WithContext(httpx.WithIdentity(r.Context(), id)))
			}
			return
		}
		id, ok := s.authenticate(w, r)
		if !ok {
			return
		}
		if id.User.MustChangePassword && !spec.allowRestricted && !spec.passwordChange {
			httpx.ErrorCode(w, http.StatusForbidden, "password_change_required", "choose a new password first")
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
	if s.cfg.Session.Binding && user.SessionBinding {
		if why := sessionMoved(sess, r.UserAgent(), httpx.IP(r.Context())); why != "" {
			// A session cookie in another browser or on another network is
			// most likely a copied one: it ends here.
			_ = s.store.DeleteSession(r.Context(), sess.IDHash)
			s.sessions.ClearCookie(w)
			s.audit.LogUser(r.Context(), &user.ID, audit.SessionMoved, user.Username, map[string]any{"reason": why, "from": sess.IP})
			httpx.ErrorCode(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
			return nil, false
		}
	}
	access, err := files.AccessFor(r.Context(), s.store, user)
	if err != nil {
		httpx.InternalError(w, r, err)
		return nil, false
	}
	return &httpx.Identity{User: user, Session: sess, Access: access}, true
}

// sessionMoved says why a session is being used from somewhere other than
// where it was started ("" if it is not): another browser, or another
// network. A browser is told apart by its User-Agent without the version
// numbers, so an update does not end the session; a network is the /16 of an
// IPv4 address or the /32 of an IPv6 one, so a changed address at the same
// provider usually does not either.
func sessionMoved(sess *store.Session, userAgent, ip string) string {
	if len(userAgent) > 256 {
		userAgent = userAgent[:256]
	}
	if sess.UserAgent != "" && versionless(sess.UserAgent) != versionless(userAgent) {
		return "browser"
	}
	from, err1 := netip.ParseAddr(sess.IP)
	now, err2 := netip.ParseAddr(ip)
	if err1 != nil || err2 != nil {
		return "" // not known at login, or not now
	}
	from, now = from.Unmap(), now.Unmap()
	bits := 16
	if from.Is6() {
		bits = 32
	}
	if p, err := from.Prefix(bits); err != nil || !p.Contains(now) {
		return "network"
	}
	return ""
}

var versionNumbers = regexp.MustCompile(`[0-9]+([._][0-9]+)*`)

func versionless(ua string) string { return versionNumbers.ReplaceAllString(ua, "") }

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
	s.hooks.Run(ctx)
	if s.r2 != nil {
		go s.r2.Run(ctx)
	}
	t := time.NewTicker(janitorEvery)
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
	s.refreshCloudflareRanges(ctx)
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
	if _, err := s.store.DeleteSharesExpiredBefore(ctx, now.AddDate(0, 0, -keepEndedDays)); err != nil {
		slog.Error("share cleanup", "err", err)
	}
	if err := s.store.DeleteAPITokensBefore(ctx, now.AddDate(0, 0, -keepEndedDays)); err != nil {
		slog.Error("token cleanup", "err", err)
	}
	if err := s.store.DeleteFileTypesBefore(ctx, now.AddDate(0, 0, -keepFileTypesDays)); err != nil {
		slog.Error("file type cleanup", "err", err)
	}
	s.uploads.Cleanup(staleUpload)
	s.pending.gc()
	s.webauthn.GC()
	if s.media != nil {
		s.media.Evict(ctx)
	}
}
