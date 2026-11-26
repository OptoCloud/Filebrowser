package store

import (
	"context"
	"time"
)

type Share struct {
	ID            int64      `json:"id"`
	TokenHash     []byte     `json:"-"`
	OwnerID       int64      `json:"owner_id"`
	Path          string     `json:"path"`
	IsDir         bool       `json:"is_dir"`
	Kind          string     `json:"kind"` // download or upload
	PasswordHash  string     `json:"-"`
	ExpiresAt     time.Time  `json:"expires_at"`
	MaxDownloads  int        `json:"max_downloads"`
	DownloadCount int        `json:"download_count"`
	CreatedAt     time.Time  `json:"created_at"`
	RevokedAt     *time.Time `json:"revoked_at"`
	// FileID identifies the shared file or folder (see files.FS.FileID).
	FileID string `json:"-"`
	// Limits of a drop link (0 or empty: the server default), and what it
	// has accepted so far, uploads in progress included.
	MaxBytes      int64    `json:"max_bytes"`
	MaxFiles      int      `json:"max_files"`
	MaxFileBytes  int64    `json:"max_file_bytes"`
	AllowedTypes  []string `json:"allowed_types"`
	SingleSession bool     `json:"single_session"`
	SessionHash   []byte   `json:"-"`
	UploadBytes   int64    `json:"upload_bytes"`
	UploadFiles   int      `json:"upload_files"`
	// Slug is a link name chosen by the owner, used in place of the token.
	Slug  string `json:"slug"`
	Title string `json:"title"`
	Note  string `json:"note"`
	// AskName makes a drop link ask uploaders for their name.
	AskName bool `json:"ask_name"`
}

const shareCols = `id, token_hash, owner_id, path, is_dir, kind, password_hash, expires_at, max_downloads, download_count, created_at, revoked_at, file_id,
	max_bytes, max_files, max_file_bytes, allowed_types, single_session, session_hash, upload_bytes, upload_files,
	slug, title, note, ask_name`

func (s *Store) CreateShare(ctx context.Context, sh *Share) error {
	err := s.pool.QueryRow(ctx, `INSERT INTO shares (token_hash, owner_id, path, is_dir, kind, password_hash, expires_at, max_downloads, file_id,
			max_bytes, max_files, max_file_bytes, allowed_types, single_session, slug, title, note, ask_name)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18) RETURNING id, created_at`,
		sh.TokenHash, sh.OwnerID, sh.Path, sh.IsDir, sh.Kind, sh.PasswordHash, sh.ExpiresAt, sh.MaxDownloads, sh.FileID,
		sh.MaxBytes, sh.MaxFiles, sh.MaxFileBytes, append([]string{}, sh.AllowedTypes...), sh.SingleSession,
		sh.Slug, sh.Title, sh.Note, sh.AskName,
	).Scan(&sh.ID, &sh.CreatedAt)
	if isUniqueViolation(err) {
		return ErrConflict // the slug is taken
	}
	return err
}

// ShareBySlug finds the share that currently has the link name slug.
func (s *Store) ShareBySlug(ctx context.Context, slug string) (*Share, error) {
	return queryOne[Share](ctx, s, `SELECT `+shareCols+` FROM shares WHERE slug = $1 AND revoked_at IS NULL`, slug)
}

func (s *Store) ShareByTokenHash(ctx context.Context, hash []byte) (*Share, error) {
	return queryOne[Share](ctx, s, `SELECT `+shareCols+` FROM shares WHERE token_hash = $1`, hash)
}

func (s *Store) ShareByID(ctx context.Context, id int64) (*Share, error) {
	return queryOne[Share](ctx, s, `SELECT `+shareCols+` FROM shares WHERE id = $1`, id)
}

// Shares lists shares; ownerID 0 lists all.
func (s *Store) Shares(ctx context.Context, ownerID int64) ([]*Share, error) {
	return queryAll[Share](ctx, s, `SELECT `+shareCols+` FROM shares WHERE ($1 = 0 OR owner_id = $1) ORDER BY created_at DESC`, ownerID)
}

// SetShareFileID records the identity of a share created before file_id
// existed, on its first use.
func (s *Store) SetShareFileID(ctx context.Context, id int64, fileID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE shares SET file_id = $2 WHERE id = $1 AND file_id = ''`, id, fileID)
	return err
}

func (s *Store) RevokeShare(ctx context.Context, id int64, at time.Time) error {
	return affected(s.pool.Exec(ctx, `UPDATE shares SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL`, id, at))
}

// CountShareDownload atomically increments the counter if the limit allows.
func (s *Store) CountShareDownload(ctx context.Context, id int64) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE shares SET download_count = download_count + 1
		WHERE id = $1 AND (max_downloads = 0 OR download_count < max_downloads)`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) DeleteSharesExpiredBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM shares WHERE expires_at < $1 OR revoked_at < $1`, cutoff)
	return tag.RowsAffected(), err
}

// ReserveShareUpload counts one more file of n bytes against a drop link,
// unless that would pass maxBytes or maxFiles (0: no limit).
func (s *Store) ReserveShareUpload(ctx context.Context, id, n, maxBytes int64, maxFiles int) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE shares SET upload_bytes = upload_bytes + $2::bigint, upload_files = upload_files + 1
		WHERE id = $1 AND ($3::bigint = 0 OR upload_bytes + $2::bigint <= $3::bigint) AND ($4::int = 0 OR upload_files < $4::int)`, id, n, maxBytes, maxFiles)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ReleaseShareUpload takes back what ReserveShareUpload counted, for an
// upload that was cancelled, refused or cleaned up.
func (s *Store) ReleaseShareUpload(ctx context.Context, id, n int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE shares SET upload_bytes = GREATEST(upload_bytes - $2::bigint, 0),
		upload_files = GREATEST(upload_files - 1, 0) WHERE id = $1`, id, n)
	return err
}

// ClaimShareSession binds a single-session link to its first browser,
// reporting whether this call was the one that did.
func (s *Store) ClaimShareSession(ctx context.Context, id int64, hash []byte) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE shares SET session_hash = $2 WHERE id = $1 AND session_hash IS NULL`, id, hash)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
