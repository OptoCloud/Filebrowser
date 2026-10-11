package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
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
}

const shareCols = `id, token_hash, owner_id, path, is_dir, kind, password_hash, expires_at, max_downloads, download_count, created_at, revoked_at`

func (s *Store) CreateShare(ctx context.Context, sh *Share) error {
	return s.pool.QueryRow(ctx, `INSERT INTO shares (token_hash, owner_id, path, is_dir, kind, password_hash, expires_at, max_downloads)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id, created_at`,
		sh.TokenHash, sh.OwnerID, sh.Path, sh.IsDir, sh.Kind, sh.PasswordHash, sh.ExpiresAt, sh.MaxDownloads,
	).Scan(&sh.ID, &sh.CreatedAt)
}

func (s *Store) shareQuery(ctx context.Context, where string, args ...any) ([]*Share, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+shareCols+` FROM shares `+where, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToAddrOfStructByPos[Share])
}

func (s *Store) ShareByTokenHash(ctx context.Context, hash []byte) (*Share, error) {
	out, err := s.shareQuery(ctx, `WHERE token_hash = $1`, hash)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, ErrNotFound
	}
	return out[0], nil
}

func (s *Store) ShareByID(ctx context.Context, id int64) (*Share, error) {
	out, err := s.shareQuery(ctx, `WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, ErrNotFound
	}
	return out[0], nil
}

// Shares lists shares; ownerID 0 lists all.
func (s *Store) Shares(ctx context.Context, ownerID int64) ([]*Share, error) {
	return s.shareQuery(ctx, `WHERE ($1 = 0 OR owner_id = $1) ORDER BY created_at DESC`, ownerID)
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
