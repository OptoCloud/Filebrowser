package server

import (
	"errors"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"filebrowser/internal/audit"
	"filebrowser/internal/auth"
	"filebrowser/internal/files"
	"filebrowser/internal/httpx"
	"filebrowser/internal/store"
)

// API tokens let scripts and other software act for a user, within a part
// of the user's access chosen when the token is made: paths, and what may
// be done there. A token is sent as "Authorization: Bearer fbt_...". It
// works only on the routes that name a capability (see token), so it can
// never open a session, reach the admin pages or change how its user signs
// in; and since it is not a cookie, no page can make a browser send it.

const tokenPrefix = "fbt_"

// Token capabilities.
const (
	capRead   = "read"   // list, search, download
	capUpload = "upload" // upload files and make folders
	capModify = "modify" // rename, move, delete
	capShare  = "share"  // make and revoke share links
)

var tokenCaps = []string{capRead, capUpload, capModify, capShare}

// token marks a route as usable with an API token that has capability c.
func token(c string) routeOpt { return func(r *routeSpec) { r.tokenCap = c } }

// bearer returns the API token a request carries, if it carries one.
func bearer(r *http.Request) (string, bool) {
	v, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return strings.TrimSpace(v), ok
}

// authenticateToken resolves the API token of a request to the route spec.
// It writes the refusal and returns false otherwise.
func (s *Server) authenticateToken(w http.ResponseWriter, r *http.Request, raw string, spec routeSpec) (*httpx.Identity, bool) {
	ctx := r.Context()
	deny := func() (*httpx.Identity, bool) {
		// Guessing is slowed per client, like share tokens.
		if !s.tokenLimiter.Allow(httpx.LimitKey(ctx), time.Now()) {
			retryAfter(w, time.Minute)
			return nil, false
		}
		httpx.ErrorCode(w, http.StatusUnauthorized, "invalid_token", "invalid or expired API token")
		return nil, false
	}
	if !strings.HasPrefix(raw, tokenPrefix) || len(raw) > 128 {
		return deny()
	}
	t, err := s.store.APITokenByHash(ctx, s.keys.HashToken(raw))
	if errors.Is(err, store.ErrNotFound) {
		return deny()
	} else if err != nil {
		httpx.InternalError(w, r, err)
		return nil, false
	}
	now := time.Now()
	if t.RevokedAt != nil || (t.ExpiresAt != nil && now.After(*t.ExpiresAt)) {
		return deny()
	}
	user, err := s.store.UserByID(ctx, t.UserID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && user.Disabled) {
		return deny()
	} else if err != nil {
		httpx.InternalError(w, r, err)
		return nil, false
	}
	ip := httpx.IP(ctx)
	if len(t.AllowedIPs) > 0 && !ipAllowed(ip, t.AllowedIPs) {
		httpx.ErrorCode(w, http.StatusForbidden, "address_not_allowed", "this API token may not be used from this address")
		return nil, false
	}
	if spec.tokenCap == "" {
		httpx.ErrorCode(w, http.StatusForbidden, "token_not_accepted", "this endpoint does not accept API tokens")
		return nil, false
	}
	if !slices.Contains(t.Caps, spec.tokenCap) {
		httpx.ErrorCode(w, http.StatusForbidden, "token_scope", "this API token is not allowed to "+spec.tokenCap)
		return nil, false
	}
	owner, err := files.AccessFor(ctx, s.store, user)
	if err != nil {
		httpx.InternalError(w, r, err)
		return nil, false
	}
	if t.LastUsedAt == nil || now.Sub(*t.LastUsedAt) > time.Minute || t.LastUsedIP != ip {
		_ = s.store.TouchAPIToken(ctx, t.ID, now, ip)
	}
	return &httpx.Identity{User: user, Token: t, Access: owner.Narrow(t.Paths, tokenWrites(t.Caps))}, true
}

// tokenWrites reports capabilities that need write access to the paths.
func tokenWrites(caps []string) bool {
	return slices.ContainsFunc(caps, func(c string) bool { return c != capRead })
}

func ipAllowed(ip string, cidrs []string) bool {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, c := range cidrs {
		if p, err := netip.ParsePrefix(c); err == nil && p.Contains(addr) {
			return true
		}
	}
	return false
}

// tokenView is a token as its lists show it.
type tokenView struct {
	*store.APIToken
	Active bool   `json:"active"`
	Owner  string `json:"owner,omitempty"`
}

func viewToken(t *store.APIToken) tokenView {
	return tokenView{APIToken: t, Active: t.RevokedAt == nil && (t.ExpiresAt == nil || time.Now().Before(*t.ExpiresAt))}
}

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.APITokens(r.Context(), httpx.IdentityFrom(r.Context()).User.ID)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	out := make([]tokenView, 0, len(list))
	for _, t := range list {
		out = append(out, viewToken(t))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string   `json:"name"`
		Paths      []string `json:"paths"`
		Caps       []string `json:"caps"`
		AllowedIPs []string `json:"allowed_ips"`
		ExpiresIn  int64    `json:"expires_in"` // seconds; 0 never expires
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	id := httpx.IdentityFrom(ctx)
	// A token is a long-lived credential: like a passkey, a stolen session
	// must not be able to mint one.
	if !requireFreshLogin(w, id) {
		return
	}
	bad := func(msg string) { httpx.Error(w, http.StatusBadRequest, msg) }
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || utf8.RuneCountInString(req.Name) > 60 {
		bad("a token needs a name of at most 60 characters")
		return
	}
	slices.Sort(req.Caps)
	req.Caps = slices.Compact(req.Caps)
	if len(req.Caps) == 0 || slices.ContainsFunc(req.Caps, func(c string) bool { return !slices.Contains(tokenCaps, c) }) {
		bad("choose what the token may do: read, upload, modify, share")
		return
	}
	if len(req.Paths) == 0 || len(req.Paths) > 50 || req.ExpiresIn < 0 || len(req.AllowedIPs) > 50 {
		bad("choose the folders the token is for")
		return
	}
	want := files.PermRead
	if tokenWrites(req.Caps) {
		want = files.PermWrite
	}
	paths := make([]string, 0, len(req.Paths))
	for _, raw := range req.Paths {
		p, err := files.Clean(raw)
		if err != nil {
			httpx.FileError(w, r, err)
			return
		}
		// '' is "all my access", whatever that is; anything else must be
		// the user's to give, now.
		if p != "" {
			if p, err = s.files.FS.Authorize(id.Access, p, want); err != nil {
				httpx.FileError(w, r, err)
				return
			}
		} else if want == files.PermWrite && !id.Access.CanWrite() {
			httpx.FileError(w, r, files.ErrForbidden)
			return
		}
		if !slices.Contains(paths, p) {
			paths = append(paths, p)
		}
	}
	ips := make([]string, 0, len(req.AllowedIPs))
	for _, raw := range req.AllowedIPs {
		raw = strings.TrimSpace(raw)
		p, err := netip.ParsePrefix(raw)
		if err != nil {
			a, aerr := netip.ParseAddr(raw)
			if aerr != nil {
				bad("allowed addresses are IP addresses or ranges like 192.168.1.0/24")
				return
			}
			p = netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen())
		}
		ips = append(ips, p.Masked().String())
	}
	raw := tokenPrefix + auth.NewToken(32)
	t := &store.APIToken{UserID: id.User.ID, Name: req.Name, TokenHash: s.keys.HashToken(raw), Hint: raw[:len(tokenPrefix)+4],
		Paths: paths, Caps: req.Caps, AllowedIPs: ips}
	if req.ExpiresIn > 0 {
		exp := time.Now().Add(time.Duration(req.ExpiresIn) * time.Second).UTC()
		t.ExpiresAt = &exp
	}
	if err := s.store.CreateAPIToken(ctx, t); err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	s.audit.Log(ctx, audit.TokenCreated, t.Name, map[string]any{"id": t.ID, "paths": t.Paths, "caps": t.Caps,
		"allowed_ips": t.AllowedIPs, "expires_at": t.ExpiresAt})
	// The token is shown this once; only its hash is kept.
	httpx.JSON(w, http.StatusOK, map[string]any{"token": raw, "info": viewToken(t)})
}

func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	s.revokeToken(w, r, httpx.IdentityFrom(r.Context()).User.ID)
}

// revokeToken revokes the token in the path; owner 0 is an admin revoking anyone's.
func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request, owner int64) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if err := s.store.RevokeAPIToken(r.Context(), owner, id, time.Now()); err != nil {
		httpx.FileError(w, r, err)
		return
	}
	s.audit.Log(r.Context(), audit.TokenRevoked, "", map[string]any{"id": id})
	httpx.OK(w)
}

func (s *Server) handleAdminTokens(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	list, err := s.store.APITokens(ctx, 0)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	names := map[int64]string{}
	for _, u := range users {
		names[u.ID] = u.Username
	}
	out := make([]tokenView, 0, len(list))
	for _, t := range list {
		v := viewToken(t)
		v.Owner = names[t.UserID]
		out = append(out, v)
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (s *Server) handleAdminRevokeToken(w http.ResponseWriter, r *http.Request) {
	s.revokeToken(w, r, 0)
}
