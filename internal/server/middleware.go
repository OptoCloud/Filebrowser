package server

import (
	"log/slog"
	"net/http"
	"net/netip"
	"runtime/debug"
	"time"

	"filebrowser/internal/httpx"
)

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		slog.Info("request", "method", r.Method, "host", r.Host, "path", httpx.RedactPath(r.URL.Path),
			"status", sw.status, "bytes", sw.bytes, "ms", time.Since(start).Milliseconds(), "ip", httpx.IP(r.Context()))
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				slog.Error("panic", "err", v, "path", httpx.RedactPath(r.URL.Path), "stack", string(debug.Stack()))
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) withIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.TrustedIPHeader == "" && !s.cfg.Dev {
			s.warnIfProxied(r)
		}
		ip := httpx.ClientIP(r, s.cfg.TrustedIPHeader, s.proxies)
		next.ServeHTTP(w, r.WithContext(httpx.WithIP(r.Context(), ip)))
	})
}

// securityHeaders sets defaults; handlers may override CSP and framing.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		if !s.cfg.Dev {
			h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

// csrf rejects state-changing requests that do not come from origin.
// SameSite=Strict cookies already block most CSRF; this is defense in depth.
func (s *Server) csrf(origin string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if o := r.Header.Get("Origin"); o != "" {
			if o != origin {
				httpx.Error(w, http.StatusForbidden, "cross-origin request rejected")
				return
			}
		} else if r.Header.Get("Sec-Fetch-Site") != "same-origin" {
			httpx.Error(w, http.StatusForbidden, "cross-origin request rejected")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// warnIfProxied warns once when requests arrive from a private or loopback
// address without trusted_ip_header: behind a reverse proxy or tunnel every
// client then shares the proxy's address and its rate limits, so a few failed
// logins from anyone would throttle everyone.
func (s *Server) warnIfProxied(r *http.Request) {
	peer, err := netip.ParseAddr(httpx.PeerAddr(r))
	if err != nil || !(peer.IsLoopback() || peer.IsPrivate()) {
		return
	}
	s.proxyWarning.Do(func() {
		slog.Warn("requests come from a proxy but trusted_ip_header is not set; all clients share one address "+
			"for rate limiting and audit logs. Set trusted_ip_header (and trusted_proxies) for your proxy or tunnel.",
			"peer", peer.String())
	})
}
