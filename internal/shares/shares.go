// Package shares implements public share links and signed file URLs.
package shares

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"filebrowser/internal/auth"
	"filebrowser/internal/files"
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
	// Slug replaces the random token in the link with a name of the owner's
	// choosing. It is as secret as it is hard to guess.
	Slug string `json:"slug"`
	// Title and Note are shown on the share page and in link previews.
	Title string `json:"title"`
	Note  string `json:"note"`
	// AskName (drop links) asks uploaders for their name; their files go
	// into a folder of that name.
	AskName bool `json:"ask_name"`
}

// slugPattern: 3 to 64 lower-case letters, digits and hyphens. It cannot be
// taken for a token, which is longer than 20 characters of mixed case.
var slugPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{1,62})[a-z0-9]$`)

const (
	maxTitleLen = 100
	maxNoteLen  = 1000
)

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
	req.Slug, req.Title, req.Note = strings.ToLower(strings.TrimSpace(req.Slug)), strings.TrimSpace(req.Title), strings.TrimSpace(req.Note)
	if req.Slug != "" && !slugPattern.MatchString(req.Slug) {
		return nil, "", InputError("a link name is 3 to 64 lower-case letters, digits and hyphens")
	}
	if utf8.RuneCountInString(req.Title) > maxTitleLen || utf8.RuneCountInString(req.Note) > maxNoteLen {
		return nil, "", InputError(fmt.Sprintf("a title is at most %d characters and a note %d", maxTitleLen, maxNoteLen))
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
		Slug: req.Slug, Title: req.Title, Note: req.Note, AskName: req.AskName && req.Kind == "upload",
	}
	if err := s.Store.CreateShare(ctx, sh); errors.Is(err, store.ErrConflict) {
		return nil, "", InputError("that link name is already in use")
	} else if err != nil {
		return nil, "", err
	}
	// The link carries the name instead of the token; the token still works.
	if sh.Slug != "" {
		token = sh.Slug
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
	if len(token) < 3 || len(token) > 64 {
		return nil, ErrInvalid
	}
	// A token, or the link name of a share that has one.
	sh, err := (*store.Share)(nil), error(store.ErrNotFound)
	if len(token) >= 20 {
		sh, err = s.Store.ShareByTokenHash(ctx, s.Keys.HashToken(token))
	}
	if errors.Is(err, store.ErrNotFound) && slugPattern.MatchString(token) {
		sh, err = s.Store.ShareBySlug(ctx, token)
	}
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
