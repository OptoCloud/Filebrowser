// Package httpx holds small HTTP helpers shared by handlers.
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"slices"
	"strings"

	"filebrowser/internal/files"
	"filebrowser/internal/store"
)

type ctxKey int

const (
	identityKey ctxKey = iota
	ipKey
)

// Identity is the authenticated caller.
type Identity struct {
	User    *store.User
	Session *store.Session
	Access  *files.Access
}

func WithIdentity(ctx context.Context, id *Identity) context.Context {
	return context.WithValue(ctx, identityKey, id)
}

func IdentityFrom(ctx context.Context) *Identity {
	id, _ := ctx.Value(identityKey).(*Identity)
	return id
}

func WithIP(ctx context.Context, ip string) context.Context { return context.WithValue(ctx, ipKey, ip) }

func IP(ctx context.Context) string {
	ip, _ := ctx.Value(ipKey).(string)
	return ip
}

// LimitKey is the client's rate-limit key: its IP address, or for IPv6 its
// /64, since one subscriber typically holds a whole /64 and could otherwise
// rotate through fresh buckets.
func LimitKey(ctx context.Context) string {
	ip := IP(ctx)
	a, err := netip.ParseAddr(ip)
	if err != nil || !a.Is6() || a.Is4In6() {
		return ip
	}
	p, _ := a.Prefix(64)
	return p.String()
}

// ClientIP returns the client address. header is believed only when the
// direct peer is one of the trusted proxies. It may hold a list
// (X-Forwarded-For: client, proxy1, ...): each proxy appends the address it
// received from, so the right-most entry that is not itself a trusted proxy is
// the client, and anything further left could have been forged by it.
func ClientIP(r *http.Request, header string, trusted []netip.Prefix) string {
	host := PeerAddr(r)
	peer, err := netip.ParseAddr(host)
	if header == "" || err != nil || !inPrefixes(peer.Unmap(), trusted) {
		return host
	}
	parts := strings.Split(strings.Join(r.Header.Values(header), ","), ",")
	for i, part := range slices.Backward(parts) {
		a, err := netip.ParseAddr(strings.TrimSpace(part))
		if err != nil {
			break
		}
		if a = a.Unmap(); i == 0 || !inPrefixes(a, trusted) {
			return a.String()
		}
	}
	return host
}

// PeerAddr is the address of the direct peer (the proxy, if there is one).
func PeerAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func inPrefixes(a netip.Addr, ps []netip.Prefix) bool {
	for _, p := range ps {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// OK writes the {"ok": true} body used by actions with nothing else to return.
func OK(w http.ResponseWriter) { JSON(w, http.StatusOK, map[string]any{"ok": true}) }

type errorBody struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

func Error(w http.ResponseWriter, status int, msg string) {
	JSON(w, status, errorBody{Error: msg})
}

func ErrorCode(w http.ResponseWriter, status int, code, msg string) {
	JSON(w, status, errorBody{Error: msg, Code: code})
}

// Decode reads a JSON body into v, rejecting unknown fields, trailing data
// and NUL characters (PostgreSQL text cannot hold them and no field needs one).
func Decode(w http.ResponseWriter, r *http.Request, v any) bool {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			Error(w, http.StatusRequestEntityTooLarge, "request body too large")
		} else {
			Error(w, http.StatusBadRequest, "invalid JSON body")
		}
		return false
	}
	if bytes.Contains(body, []byte(`\u0000`)) {
		Error(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		Error(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	if dec.More() {
		Error(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

// FileError maps files/store errors to HTTP responses.
func FileError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case r.Context().Err() != nil && errors.Is(err, r.Context().Err()):
		// The client went away; nothing went wrong on this side.
		Error(w, http.StatusServiceUnavailable, "request cancelled")
	case errors.Is(err, files.ErrNotFound), errors.Is(err, store.ErrNotFound):
		Error(w, http.StatusNotFound, "not found")
	case errors.Is(err, files.ErrForbidden):
		Error(w, http.StatusForbidden, "forbidden")
	case errors.Is(err, files.ErrBadPath):
		Error(w, http.StatusBadRequest, "invalid path or name")
	case errors.Is(err, files.ErrExists), errors.Is(err, store.ErrConflict):
		Error(w, http.StatusConflict, "already exists")
	case errors.Is(err, files.ErrIsDir):
		Error(w, http.StatusBadRequest, "is a directory")
	case errors.Is(err, files.ErrNotDir):
		Error(w, http.StatusBadRequest, "not a directory")
	default:
		InternalError(w, r, err)
	}
}

// Bearer tokens in URL paths (share links, signed links, invites).
var tokenInPath = regexp.MustCompile(`^/(s|x|invite)/[^/]+`)

// RedactPath hides bearer tokens in a URL path before it is logged.
func RedactPath(path string) string {
	return tokenInPath.ReplaceAllString(path, "/$1/***")
}

func InternalError(w http.ResponseWriter, r *http.Request, err error) {
	slog.ErrorContext(r.Context(), "internal error", "method", r.Method, "path", RedactPath(r.URL.Path), "err", err)
	Error(w, http.StatusInternalServerError, "internal error")
}

// CleanPath cleans a client-supplied path, writing the error response if it
// is invalid.
func CleanPath(w http.ResponseWriter, r *http.Request, raw string) (string, bool) {
	p, err := files.Clean(raw)
	if err != nil {
		FileError(w, r, err)
		return "", false
	}
	return p, true
}

// QueryPath reads and cleans a path query parameter.
func QueryPath(w http.ResponseWriter, r *http.Request, name string) (string, bool) {
	return CleanPath(w, r, r.URL.Query().Get(name))
}

// CleanPaths cleans a list of client paths, rejecting empty lists.
func CleanPaths(w http.ResponseWriter, r *http.Request, in []string) ([]string, bool) {
	if len(in) == 0 || len(in) > 10000 {
		Error(w, http.StatusBadRequest, "no paths given")
		return nil, false
	}
	out := make([]string, len(in))
	for i, p := range in {
		c, err := files.Clean(p)
		if err != nil {
			FileError(w, r, err)
			return nil, false
		}
		out[i] = c
	}
	return out, true
}

// ContentDisposition builds an RFC 6266 header value.
func ContentDisposition(kind, name string) string {
	ascii := strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' || r == '%' || r == ';' {
			return '_'
		}
		return r
	}, name)
	return kind + `; filename="` + ascii + `"; filename*=UTF-8''` + PathEscape(name)
}

// PathEscape percent-encodes everything except RFC 3986 unreserved characters,
// which makes the result valid both as a URL path segment and as an RFC 5987
// ext-value.
func PathEscape(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("-._~", c) >= 0 {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
}

// UserContentHeaders sets the restrictive headers used for every response
// carrying user file bytes.
func UserContentHeaders(w http.ResponseWriter, contentType string, inline bool, name string) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	if inline {
		h.Set("Content-Type", contentType)
		h.Set("Content-Disposition", ContentDisposition("inline", name))
		if contentType == "application/pdf" {
			h.Set("Content-Security-Policy", "default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'")
		} else {
			h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
		}
	} else {
		h.Set("Content-Type", "application/octet-stream")
		h.Set("Content-Disposition", ContentDisposition("attachment", name))
		h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	}
}
