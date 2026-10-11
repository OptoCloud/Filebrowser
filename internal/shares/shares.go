// Package shares implements public share links and signed file URLs.
package shares

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"filebrowser/internal/auth"
	"filebrowser/internal/files"
	"filebrowser/internal/store"
)

var (
	ErrInvalid = errors.New("share not found or expired")
	ErrLocked  = errors.New("share requires password")
	ErrLimit   = errors.New("download limit reached")
)

type Service struct {
	Store  *store.Store
	Keys   *auth.Keys
	Files  *files.Service
	Secure bool
	// ShareOrigin is the public origin used to build links.
	ShareOrigin   string
	DefaultExpiry time.Duration
	MaxExpiry     time.Duration
}

type CreateRequest struct {
	Path         string `json:"path"`
	Kind         string `json:"kind"` // download (default) or upload
	ExpiresIn    int64  `json:"expires_in"`
	Password     string `json:"password"`
	MaxDownloads int    `json:"max_downloads"`
}

// Create makes a new share. The plaintext token is returned once and never stored.
func (s *Service) Create(ctx context.Context, owner *store.User, a *files.Access, req CreateRequest) (*store.Share, string, error) {
	p, err := files.Clean(req.Path)
	if err != nil {
		return nil, "", err
	}
	if req.Kind == "" {
		req.Kind = "download"
	}
	if req.Kind != "download" && req.Kind != "upload" {
		return nil, "", fmt.Errorf("%w: kind must be download or upload", files.ErrBadPath)
	}
	// Creating any share requires write access to the shared path. The share
	// stores the real (symlink-resolved) path.
	p, err = s.Files.FS.Authorize(a, p, files.PermWrite)
	if err != nil {
		return nil, "", err
	}
	fi, err := s.Files.FS.Stat(p)
	if err != nil {
		return nil, "", err
	}
	if req.Kind == "upload" && !fi.IsDir() {
		return nil, "", files.ErrNotDir
	}
	exp := time.Duration(req.ExpiresIn) * time.Second
	if exp <= 0 {
		exp = s.DefaultExpiry
	}
	if exp > s.MaxExpiry {
		exp = s.MaxExpiry
	}
	if req.MaxDownloads < 0 {
		req.MaxDownloads = 0
	}
	var pwHash string
	if req.Password != "" {
		if len(req.Password) > auth.MaxPasswordLen {
			return nil, "", fmt.Errorf("%w: password too long", files.ErrBadPath)
		}
		if pwHash, err = auth.HashPassword(req.Password); err != nil {
			return nil, "", err
		}
	}
	token := auth.NewToken(18) // 144 bits
	sh := &store.Share{
		TokenHash:    s.Keys.HashToken(token),
		OwnerID:      owner.ID,
		Path:         p,
		IsDir:        fi.IsDir(),
		Kind:         req.Kind,
		PasswordHash: pwHash,
		ExpiresAt:    time.Now().Add(exp).UTC(),
		MaxDownloads: req.MaxDownloads,
	}
	if err := s.Store.CreateShare(ctx, sh); err != nil {
		return nil, "", err
	}
	return sh, token, nil
}

func (s *Service) URL(token string) string { return s.ShareOrigin + "/s/" + token }

// Resolved is a valid share together with its owner's current access.
type Resolved struct {
	Share *store.Share
	Owner *store.User
	// Access is the owner's access, re-evaluated on every request so that
	// revoking a grant also disables the owner's shares.
	Access *files.Access
	// Root is the real path of the share root; everything served stays within it.
	Root string

	fs *files.FS
}

// Resolve validates a token. It returns ErrInvalid for unknown, revoked or
// expired shares and for shares whose owner lost access.
func (s *Service) Resolve(ctx context.Context, token string) (*Resolved, error) {
	if len(token) < 20 || len(token) > 64 {
		return nil, ErrInvalid
	}
	sh, err := s.Store.ShareByTokenHash(ctx, s.Keys.HashToken(token))
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrInvalid
	} else if err != nil {
		return nil, err
	}
	if sh.RevokedAt != nil || time.Now().After(sh.ExpiresAt) {
		return nil, ErrInvalid
	}
	owner, err := s.Store.UserByID(ctx, sh.OwnerID)
	if err != nil || owner.Disabled {
		return nil, ErrInvalid
	}
	grants, err := s.Store.Grants(ctx, owner.ID)
	if err != nil {
		return nil, err
	}
	a := &files.Access{Admin: owner.IsAdmin(), Grants: grants}
	want := files.PermRead
	if sh.Kind == "upload" {
		want = files.PermWrite
	}
	root, err := s.Files.FS.Authorize(a, sh.Path, want)
	if err != nil {
		return nil, ErrInvalid
	}
	return &Resolved{Share: sh, Owner: owner, Access: a, Root: root, fs: s.Files.FS}, nil
}

// Sub resolves a client path relative to the share root and returns its real
// path. Symlinks may not lead outside the share or the owner's access. File
// shares accept only the empty path.
func (r *Resolved) Sub(p string) (string, error) {
	c, err := files.Clean(p)
	if err != nil {
		return "", err
	}
	if c != "" && !r.Share.IsDir {
		return "", files.ErrNotFound
	}
	real, err := r.fs.Resolve(files.Join(r.Root, c))
	if err != nil {
		return "", err
	}
	if !r.Allow(real) {
		return "", files.ErrNotFound
	}
	return real, nil
}

// Allow reports whether a real path may be served through this share.
func (r *Resolved) Allow(real string) bool {
	return files.Within(real, r.Root) && r.Access.Perm(real) >= files.PermRead
}

// Rel converts a real path back to share-relative form.
func (r *Resolved) Rel(full string) string {
	if r.Root == "" {
		return full
	}
	return strings.TrimPrefix(strings.TrimPrefix(full, r.Root), "/")
}

func (s *Service) unlockCookieName(id int64) string {
	if s.Secure {
		return "__Host-fbs" + strconv.FormatInt(id, 10)
	}
	return "fbs" + strconv.FormatInt(id, 10)
}

func (s *Service) unlockMsg(sh *store.Share, exp int64) []byte {
	return []byte(fmt.Sprintf("%d|%d|%s", sh.ID, exp, sh.PasswordHash))
}

// Unlocked reports whether r carries a valid unlock cookie (or no password is set).
func (s *Service) Unlocked(r *http.Request, sh *store.Share) bool {
	if sh.PasswordHash == "" {
		return true
	}
	c, err := r.Cookie(s.unlockCookieName(sh.ID))
	if err != nil {
		return false
	}
	expStr, sigStr, ok := strings.Cut(c.Value, ".")
	if !ok {
		return false
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigStr)
	return err == nil && s.Keys.Verify("share-unlock", s.unlockMsg(sh, exp), sig)
}

// Unlock verifies the password and sets the unlock cookie.
func (s *Service) Unlock(w http.ResponseWriter, sh *store.Share, password string) bool {
	if sh.PasswordHash == "" {
		return true
	}
	if !auth.VerifyPassword(sh.PasswordHash, password) {
		return false
	}
	exp := min(time.Now().Add(12*time.Hour).Unix(), sh.ExpiresAt.Unix())
	sig := s.Keys.Sign("share-unlock", s.unlockMsg(sh, exp))
	http.SetCookie(w, &http.Cookie{ // #nosec G124 nosemgrep -- Secure is off only with dev = true (http://localhost)
		Name:     s.unlockCookieName(sh.ID),
		Value:    strconv.FormatInt(exp, 10) + "." + base64.RawURLEncoding.EncodeToString(sig),
		Path:     "/",
		Expires:  time.Unix(exp, 0),
		HttpOnly: true,
		Secure:   s.Secure,
		SameSite: http.SameSiteLaxMode,
	})
	return true
}

// Link is the payload of a short-lived signed URL on the share origin, used
// for "Open in VLC" and for PDF previews.
type Link struct {
	UserID  int64  `json:"u"`
	Path    string `json:"p"`
	Variant string `json:"v,omitempty"`
	Inline  bool   `json:"i,omitempty"`
	Expires int64  `json:"e"`
}

func (s *Service) SignLink(l Link) string {
	raw, _ := json.Marshal(l)
	sig := s.Keys.Sign("link", raw)
	enc := base64.RawURLEncoding
	return enc.EncodeToString(raw) + "." + enc.EncodeToString(sig[:20])
}

func (s *Service) VerifyLink(tok string) (*Link, error) {
	enc := base64.RawURLEncoding
	a, b, ok := strings.Cut(tok, ".")
	if !ok {
		return nil, ErrInvalid
	}
	raw, err1 := enc.DecodeString(a)
	sig, err2 := enc.DecodeString(b)
	if err1 != nil || err2 != nil || len(sig) != 20 {
		return nil, ErrInvalid
	}
	full := s.Keys.Sign("link", raw)
	if !auth.ConstantTimeEqual(full[:20], sig) {
		return nil, ErrInvalid
	}
	var l Link
	if err := json.Unmarshal(raw, &l); err != nil || time.Now().Unix() > l.Expires {
		return nil, ErrInvalid
	}
	return &l, nil
}

// LinkURL builds the public URL for a signed link; the file name is appended
// so external players show a sensible title.
func (s *Service) LinkURL(l Link, name string) string {
	return s.ShareOrigin + "/x/" + s.SignLink(l) + "/" + pathEscape(name)
}

func pathEscape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("-._~", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func (s *Service) countedCookieName(id int64) string {
	if s.Secure {
		return "__Host-fbc" + strconv.FormatInt(id, 10)
	}
	return "fbc" + strconv.FormatInt(id, 10)
}

// Counted reports whether this browser has already used a download of sh.
func (s *Service) Counted(r *http.Request, sh *store.Share) bool {
	c, err := r.Cookie(s.countedCookieName(sh.ID))
	if err != nil {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(c.Value)
	return err == nil && s.Keys.Verify("share-counted", []byte(strconv.FormatInt(sh.ID, 10)), sig)
}

// MarkCounted records that this browser has used a download of sh.
func (s *Service) MarkCounted(w http.ResponseWriter, sh *store.Share) {
	sig := s.Keys.Sign("share-counted", []byte(strconv.FormatInt(sh.ID, 10)))
	http.SetCookie(w, &http.Cookie{ // #nosec G124 nosemgrep -- Secure is off only with dev = true (http://localhost)
		Name: s.countedCookieName(sh.ID), Value: base64.RawURLEncoding.EncodeToString(sig), Path: "/",
		Expires: sh.ExpiresAt, HttpOnly: true, Secure: s.Secure, SameSite: http.SameSiteLaxMode,
	})
}
