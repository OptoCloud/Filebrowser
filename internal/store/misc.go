package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Invites

type Invite struct {
	ID        int64      `json:"id"`
	TokenHash []byte     `json:"-"`
	Role      string     `json:"role"`
	GrantPath *string    `json:"grant_path"`
	GrantPerm *string    `json:"grant_perm"`
	CreatedBy *int64     `json:"created_by"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	UsedAt    *time.Time `json:"used_at"`
	UsedBy    *int64     `json:"used_by"`
}

const inviteCols = `id, token_hash, role, grant_path, grant_perm, created_by, created_at, expires_at, used_at, used_by`

func (s *Store) CreateInvite(ctx context.Context, inv *Invite) error {
	return s.pool.QueryRow(ctx, `INSERT INTO invites (token_hash, role, grant_path, grant_perm, created_by, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6) RETURNING id, created_at`,
		inv.TokenHash, inv.Role, inv.GrantPath, inv.GrantPerm, inv.CreatedBy, inv.ExpiresAt).Scan(&inv.ID, &inv.CreatedAt)
}

func (s *Store) InviteByTokenHash(ctx context.Context, hash []byte) (*Invite, error) {
	return queryOne[Invite](ctx, s, `SELECT `+inviteCols+` FROM invites WHERE token_hash = $1`, hash)
}

func (s *Store) Invites(ctx context.Context) ([]*Invite, error) {
	return queryAll[Invite](ctx, s, `SELECT `+inviteCols+` FROM invites ORDER BY created_at DESC`)
}

func (s *Store) DeleteInvite(ctx context.Context, id int64) error {
	return affected(s.pool.Exec(ctx, `DELETE FROM invites WHERE id = $1`, id))
}

// AcceptInvite creates the user (and grant) and marks the invite used, atomically.
func (s *Store) AcceptInvite(ctx context.Context, inviteID int64, username, passwordHash string, now time.Time) (*User, error) {
	var user *User
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var inv Invite
		err := tx.QueryRow(ctx, `SELECT role, grant_path, grant_perm FROM invites
			WHERE id = $1 AND used_at IS NULL AND expires_at > $2 FOR UPDATE`, inviteID, now).
			Scan(&inv.Role, &inv.GrantPath, &inv.GrantPerm)
		if err != nil {
			return notFound(err)
		}
		user, err = scanUser(tx.QueryRow(ctx,
			`INSERT INTO users (username, password_hash, role) VALUES ($1, $2, $3) RETURNING `+userCols,
			username, passwordHash, inv.Role))
		if isUniqueViolation(err) {
			return ErrConflict
		} else if err != nil {
			return err
		}
		if inv.GrantPath != nil && inv.GrantPerm != nil {
			if _, err := tx.Exec(ctx, `INSERT INTO grants (user_id, path, permission) VALUES ($1, $2, $3)`,
				user.ID, *inv.GrantPath, *inv.GrantPerm); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `UPDATE invites SET used_at = $2, used_by = $3 WHERE id = $1`, inviteID, now, user.ID)
		return err
	})
	return user, err
}

// Trash

type TrashItem struct {
	ID           string    `json:"id"`
	UserID       int64     `json:"user_id"`
	OriginalPath string    `json:"original_path"`
	TrashPath    string    `json:"-"`
	IsDir        bool      `json:"is_dir"`
	Size         int64     `json:"size"`
	TrashedAt    time.Time `json:"trashed_at"`
}

const trashCols = `id, user_id, original_path, trash_path, is_dir, size, trashed_at`

func (s *Store) AddTrash(ctx context.Context, t *TrashItem) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO trash (`+trashCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		t.ID, t.UserID, t.OriginalPath, t.TrashPath, t.IsDir, t.Size, t.TrashedAt)
	return err
}

func (s *Store) TrashItems(ctx context.Context, userID int64) ([]*TrashItem, error) {
	return queryAll[TrashItem](ctx, s, `SELECT `+trashCols+` FROM trash WHERE user_id = $1 ORDER BY trashed_at DESC`, userID)
}

func (s *Store) TrashItem(ctx context.Context, userID int64, id string) (*TrashItem, error) {
	return queryOne[TrashItem](ctx, s, `SELECT `+trashCols+` FROM trash WHERE user_id = $1 AND id = $2`, userID, id)
}

func (s *Store) TrashOlderThan(ctx context.Context, cutoff time.Time) ([]*TrashItem, error) {
	return queryAll[TrashItem](ctx, s, `SELECT `+trashCols+` FROM trash WHERE trashed_at < $1`, cutoff)
}

func (s *Store) DeleteTrash(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM trash WHERE id = $1`, id)
	return err
}

// Media caches, keyed by path + size + mtime.

func (s *Store) Probe(ctx context.Context, path string, size, mtime int64) (json.RawMessage, error) {
	var raw json.RawMessage
	err := s.pool.QueryRow(ctx, `SELECT probe_json FROM media_probe WHERE path = $1 AND size = $2 AND mtime = $3`,
		path, size, mtime).Scan(&raw)
	return raw, notFound(err)
}

func (s *Store) PutProbe(ctx context.Context, path string, size, mtime int64, raw json.RawMessage) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO media_probe (path, size, mtime, probe_json) VALUES ($1,$2,$3,$4)
		ON CONFLICT (path) DO UPDATE SET size = excluded.size, mtime = excluded.mtime,
		probe_json = excluded.probe_json, probed_at = now()`, path, size, mtime, raw)
	return err
}

type MediaCacheEntry struct {
	SourcePath  string
	SourceSize  int64
	SourceMtime int64
	Variant     string
	CachePath   string
	Bytes       int64
	LastUsedAt  time.Time
}

const mediaCacheCols = `source_path, source_size, source_mtime, variant, cache_path, bytes, last_used_at`

func (s *Store) MediaCache(ctx context.Context, path string, size, mtime int64, variant string) (*MediaCacheEntry, error) {
	return queryOne[MediaCacheEntry](ctx, s, `SELECT `+mediaCacheCols+` FROM media_cache
		WHERE source_path = $1 AND source_size = $2 AND source_mtime = $3 AND variant = $4`, path, size, mtime, variant)
}

// PutMediaCache stores an entry and returns the previous cache path for the
// same source+variant (if any, and different), so the caller can delete it.
func (s *Store) PutMediaCache(ctx context.Context, e *MediaCacheEntry) (old string, err error) {
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT cache_path FROM media_cache WHERE source_path = $1 AND variant = $2`,
			e.SourcePath, e.Variant).Scan(&old)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO media_cache (`+mediaCacheCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (source_path, variant) DO UPDATE SET source_size = excluded.source_size,
			source_mtime = excluded.source_mtime, cache_path = excluded.cache_path, bytes = excluded.bytes,
			last_used_at = excluded.last_used_at`,
			e.SourcePath, e.SourceSize, e.SourceMtime, e.Variant, e.CachePath, e.Bytes, e.LastUsedAt)
		return err
	})
	if old == e.CachePath {
		old = ""
	}
	return old, err
}

func (s *Store) TouchMediaCache(ctx context.Context, path, variant string, at time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE media_cache SET last_used_at = $3 WHERE source_path = $1 AND variant = $2`,
		path, variant, at)
	return err
}

func (s *Store) MediaCacheTotal(ctx context.Context) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(sum(bytes), 0) FROM media_cache`).Scan(&n)
	return n, err
}

func (s *Store) MediaCacheLRU(ctx context.Context, limit int) ([]*MediaCacheEntry, error) {
	return queryAll[MediaCacheEntry](ctx, s, `SELECT `+mediaCacheCols+` FROM media_cache ORDER BY last_used_at LIMIT $1`, limit)
}

func (s *Store) DeleteMediaCache(ctx context.Context, path, variant string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM media_cache WHERE source_path = $1 AND variant = $2`, path, variant)
	return err
}

func (s *Store) Thumbnail(ctx context.Context, path string, size, mtime int64) (string, error) {
	var p string
	err := s.pool.QueryRow(ctx, `SELECT thumb_path FROM thumbnails WHERE path = $1 AND size = $2 AND mtime = $3`,
		path, size, mtime).Scan(&p)
	return p, notFound(err)
}

// PutThumbnail returns the previous thumbnail file path if it differs.
func (s *Store) PutThumbnail(ctx context.Context, path string, size, mtime int64, thumbPath string) (string, error) {
	var old string
	err := s.pool.QueryRow(ctx, `WITH prev AS (SELECT thumb_path FROM thumbnails WHERE path = $1)
		INSERT INTO thumbnails (path, size, mtime, thumb_path) VALUES ($1,$2,$3,$4)
		ON CONFLICT (path) DO UPDATE SET size = excluded.size, mtime = excluded.mtime,
		thumb_path = excluded.thumb_path, created_at = now()
		RETURNING COALESCE((SELECT thumb_path FROM prev), '')`, path, size, mtime, thumbPath).Scan(&old)
	if old == thumbPath {
		old = ""
	}
	return old, err
}

// Audit log

type AuditEntry struct {
	ID       int64           `json:"id"`
	TS       time.Time       `json:"ts"`
	UserID   *int64          `json:"user_id"`
	Username *string         `json:"username"`
	IP       string          `json:"ip"`
	Action   string          `json:"action"`
	Target   string          `json:"target"`
	Details  json.RawMessage `json:"details"`
}

func (s *Store) AddAudit(ctx context.Context, userID *int64, ip, action, target string, details any) error {
	var raw []byte
	if details != nil {
		var err error
		if raw, err = json.Marshal(details); err != nil {
			return err
		}
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO audit_log (user_id, ip, action, target, details_json) VALUES ($1,$2,$3,$4,$5)`,
		userID, ip, action, target, raw)
	return err
}

type AuditQuery struct {
	BeforeID int64
	Action   string
	UserID   int64
	Limit    int
}

func (s *Store) Audit(ctx context.Context, q AuditQuery) ([]*AuditEntry, error) {
	if q.Limit <= 0 || q.Limit > 500 {
		q.Limit = 100
	}
	return queryAll[AuditEntry](ctx, s, `SELECT a.id, a.ts, a.user_id, u.username, a.ip, a.action, a.target, a.details_json
		FROM audit_log a LEFT JOIN users u ON u.id = a.user_id
		WHERE ($1 = 0 OR a.id < $1) AND ($2 = '' OR a.action LIKE $2 || '%') AND ($3 = 0 OR a.user_id = $3)
		ORDER BY a.id DESC LIMIT $4`, q.BeforeID, q.Action, q.UserID, q.Limit)
}
