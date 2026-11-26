// Package shares implements public share links and signed file URLs.
package shares

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"filebrowser/internal/auth"
	"filebrowser/internal/files"
	"filebrowser/internal/httpx"
	"filebrowser/internal/store"
)

var (
	ErrInvalid = errors.New("share not found or expired")
	ErrLocked  = errors.New("share requires password")
	ErrLimit   = errors.New("download limit reached")
)

// MinPasswordLen is the shortest share password accepted, the same as for
// accounts. Share links are public, so their passwords face guessing from
// anywhere.
const MinPasswordLen = 10

// InputError is a mistake in a CreateRequest; its message is shown to the user.
type InputError string

func (e InputError) Error() string { return string(e) }

type Service struct {
	Store  *store.Store
	Keys   *auth.Keys
	Files  *files.Service
	Secure bool
	// ShareOrigin is the public origin used to build links.
	ShareOrigin   string
	DefaultExpiry time.Duration
	MaxExpiry     time.Duration
	// DropMaxBytes and DropMaxFiles cap a drop link that sets no limit of
	// its own; UploadMaxSize is the largest upload the server takes.
	DropMaxBytes  int64
	DropMaxFiles  int
	UploadMaxSize int64
}

type CreateRequest struct {
	Path         string `json:"path"`
	Kind         string `json:"kind"` // download (default) or upload
	ExpiresIn    int64  `json:"expires_in"`
	Password     string `json:"password"`
	MaxDownloads int    `json:"max_downloads"`
	// Drop links only: what the link accepts (0 or empty: server default).
	MaxBytes      int64    `json:"max_bytes"`
	MaxFiles      int      `json:"max_files"`
	MaxFileBytes  int64    `json:"max_file_bytes"`
	AllowedTypes  []string `json:"allowed_types"`
	SingleSession bool     `json:"single_session"`
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
		return nil, "", InputError("kind must be download or upload")
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
	if req.Kind != "upload" {
		req.MaxBytes, req.MaxFiles, req.MaxFileBytes, req.AllowedTypes, req.SingleSession = 0, 0, 0, nil, false
	}
	if req.MaxBytes < 0 || req.MaxFiles < 0 || req.MaxFileBytes < 0 {
		return nil, "", InputError("limits cannot be negative")
	}
	for _, t := range req.AllowedTypes {
		if !slices.Contains(files.Categories, t) {
			return nil, "", InputError("unknown file type " + strconv.Quote(t))
		}
	}
	var pwHash string
	if req.Password != "" {
		if n := utf8.RuneCountInString(req.Password); n < MinPasswordLen || len(req.Password) > auth.MaxPasswordLen {
			return nil, "", InputError(fmt.Sprintf("share password must be %d to %d characters", MinPasswordLen, auth.MaxPasswordLen))
		}
		if pwHash, err = auth.HashPassword(req.Password); err != nil {
			return nil, "", err
		}
	}
	fileID, err := s.Files.FS.FileID(p)
	if err != nil {
		return nil, "", err
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
		FileID:       fileID,
		MaxBytes:     req.MaxBytes, MaxFiles: req.MaxFiles, MaxFileBytes: req.MaxFileBytes,
		AllowedTypes: slices.Compact(slices.Sorted(slices.Values(req.AllowedTypes))), SingleSession: req.SingleSession,
	}
	if err := s.Store.CreateShare(ctx, sh); err != nil {
		return nil, "", err
	}
	return sh, token, nil
}

// Limits are what a drop link accepts, with the server defaults filled in;
// 0 still means no limit.
type Limits struct {
	Bytes     int64 // in total
	Files     int   // in total
	FileBytes int64 // per file
	Types     []string
}

func (s *Service) Limits(sh *store.Share) Limits {
	l := Limits{Bytes: sh.MaxBytes, Files: sh.MaxFiles, FileBytes: sh.MaxFileBytes, Types: sh.AllowedTypes}
	if l.Bytes == 0 {
		l.Bytes = s.DropMaxBytes
	}
	if l.Files == 0 {
		l.Files = s.DropMaxFiles
	}
	if l.FileBytes == 0 || (s.UploadMaxSize > 0 && l.FileBytes > s.UploadMaxSize) {
		l.FileBytes = s.UploadMaxSize
	}
	return l
}

// Allows reports whether a file of the media type mime may be dropped.
func (l Limits) Allows(mime string) bool {
	return len(l.Types) == 0 || slices.Contains(l.Types, files.Category(mime))
}

func (s *Service) sessionCookieName(id int64) string {
	if s.Secure {
		return "__Host-fbd" + strconv.FormatInt(id, 10)
	}
	return "fbd" + strconv.FormatInt(id, 10)
}

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
	a, err := files.AccessFor(ctx, s.Store, owner)
	if err != nil {
		return nil, err
	}
	want := files.PermRead
	if sh.Kind == "upload" {
		want = files.PermWrite
	}
	root, err := s.Files.FS.Authorize(a, sh.Path, want)
	if err != nil {
		return nil, ErrInvalid
	}
	// A share is for the file it was made for, not whatever is at its path
	// now: after a delete or rename, a new file there must not be served.
	fileID, err := s.Files.FS.FileID(root)
	if err != nil {
		return nil, ErrInvalid
	}
	switch {
	case sh.FileID == "" && fileID != "":
		if err := s.Store.SetShareFileID(ctx, sh.ID, fileID); err != nil {
			return nil, err
		}
	case sh.FileID != fileID:
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
	auth.SetCookie(w, auth.Cookie{
		Name:     s.unlockCookieName(sh.ID),
		Value:    strconv.FormatInt(exp, 10) + "." + base64.RawURLEncoding.EncodeToString(sig),
		Expires:  time.Unix(exp, 0),
		SameSite: http.SameSiteLaxMode,
	}, s.Secure)
	return true
}

// Link is the payload of a short-lived signed URL on the share origin, used
// for "Open in VLC" and for PDF previews.
type Link struct {
	UserID int64 `json:"u"`
	// Session is the hash of the session that made the link: logging out (or
	// any other end of that session) revokes the link too.
	Session []byte `json:"s"`
	Path    string `json:"p"`
	// FileID is the identity of the file at Path when the link was made (see
	// files.FS.FileID): the link is for that file, not for whatever is later
	// uploaded or moved to the same path.
	FileID  string `json:"f,omitempty"`
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

// LinkURL builds the public URL for a signed link to the file now at l.Path
// (a real path); the file name is appended so external players show a
// sensible title.
func (s *Service) LinkURL(l Link, name string) (string, error) {
	id, err := s.Files.FS.FileID(l.Path)
	if err != nil {
		return "", err
	}
	l.FileID = id
	return s.linkURL(l, name), nil
}

// SameFile reports whether the file at the real path p is still the one l was
// made for.
func (s *Service) SameFile(l *Link, p string) bool {
	id, err := s.Files.FS.FileID(p)
	return err == nil && id == l.FileID
}

func (s *Service) linkURL(l Link, name string) string {
	return s.ShareOrigin + "/x/" + s.SignLink(l) + "/" + httpx.PathEscape(name)
}

func (s *Service) countedCookieName(id int64) string {
	if s.Secure {
		return "__Host-fbc" + strconv.FormatInt(id, 10)
	}
	return "fbc" + strconv.FormatInt(id, 10)
}

// countedWindow is how long a browser that used a download of a limited
// link may keep using it (seeking in a video, resuming, listing again)
// without using another. The cookie is not tied to one browser, so the
// window is what bounds a copy of it handed to someone else.
const countedWindow = 24 * time.Hour

func countedMsg(sh *store.Share, exp int64) []byte {
	return []byte(strconv.FormatInt(sh.ID, 10) + "|" + strconv.FormatInt(exp, 10))
}

// Counted reports whether this browser has used a download of sh recently
// enough to go on without using another.
func (s *Service) Counted(r *http.Request, sh *store.Share) bool {
	c, err := r.Cookie(s.countedCookieName(sh.ID))
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
	return err == nil && s.Keys.Verify("share-counted", countedMsg(sh, exp), sig)
}

// MarkCounted records that this browser has used a download of sh.
func (s *Service) MarkCounted(w http.ResponseWriter, sh *store.Share) {
	exp := min(time.Now().Add(countedWindow).Unix(), sh.ExpiresAt.Unix())
	sig := s.Keys.Sign("share-counted", countedMsg(sh, exp))
	auth.SetCookie(w, auth.Cookie{
		Name:    s.countedCookieName(sh.ID),
		Value:   strconv.FormatInt(exp, 10) + "." + base64.RawURLEncoding.EncodeToString(sig),
		Expires: time.Unix(exp, 0), SameSite: http.SameSiteLaxMode,
	}, s.Secure)
}
