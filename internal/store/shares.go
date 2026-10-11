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
}

const shareCols = `id, token_hash, owner_id, path, is_dir, kind, password_hash, expires_at, max_downloads, download_count, created_at, revoked_at, file_id`

func (s *Store) CreateShare(ctx context.Context, sh *Share) error {
	return s.pool.QueryRow(ctx, `INSERT INTO shares (token_hash, owner_id, path, is_dir, kind, password_hash, expires_at, max_downloads, file_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id, created_at`,
		sh.TokenHash, sh.OwnerID, sh.Path, sh.IsDir, sh.Kind, sh.PasswordHash, sh.ExpiresAt, sh.MaxDownloads, sh.FileID,
	).Scan(&sh.ID, &sh.CreatedAt)
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
