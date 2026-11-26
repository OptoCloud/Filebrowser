package store

import (
	"context"
	"time"
)

// APIToken is a credential for scripts and other software (see migration
// 0008). The token itself is only ever held by its user; TokenHash is kept.
type APIToken struct {
	ID         int64      `json:"id"`
	UserID     int64      `json:"user_id"`
	Name       string     `json:"name"`
	TokenHash  []byte     `json:"-"`
	Hint       string     `json:"hint"`
	Paths      []string   `json:"paths"`
	Caps       []string   `json:"caps"`
	AllowedIPs []string   `json:"allowed_ips"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	LastUsedIP string     `json:"last_used_ip"`
	RevokedAt  *time.Time `json:"revoked_at"`
}

const tokenCols = `id, user_id, name, token_hash, hint, paths, caps, allowed_ips, created_at, expires_at, last_used_at, last_used_ip, revoked_at`

func (s *Store) CreateAPIToken(ctx context.Context, t *APIToken) error {
	return s.pool.QueryRow(ctx, `INSERT INTO api_tokens (user_id, name, token_hash, hint, paths, caps, allowed_ips, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id, created_at`,
		t.UserID, t.Name, t.TokenHash, t.Hint, append([]string{}, t.Paths...), append([]string{}, t.Caps...),
		append([]string{}, t.AllowedIPs...), t.ExpiresAt,
	).Scan(&t.ID, &t.CreatedAt)
}

func (s *Store) APITokenByHash(ctx context.Context, hash []byte) (*APIToken, error) {
	return queryOne[APIToken](ctx, s, `SELECT `+tokenCols+` FROM api_tokens WHERE token_hash = $1`, hash)
}

// APITokens lists tokens, newest first; userID 0 lists everyone's.
func (s *Store) APITokens(ctx context.Context, userID int64) ([]*APIToken, error) {
	return queryAll[APIToken](ctx, s, `SELECT `+tokenCols+` FROM api_tokens
		WHERE ($1 = 0 OR user_id = $1) ORDER BY created_at DESC`, userID)
}

// RevokeAPIToken revokes a token; userID 0 revokes whoever's it is.
func (s *Store) RevokeAPIToken(ctx context.Context, userID, id int64, at time.Time) error {
	return affected(s.pool.Exec(ctx, `UPDATE api_tokens SET revoked_at = $3
		WHERE id = $2 AND ($1 = 0 OR user_id = $1) AND revoked_at IS NULL`, userID, id, at))
}

// TouchAPIToken records a use of the token.
func (s *Store) TouchAPIToken(ctx context.Context, id int64, at time.Time, ip string) error {
	_, err := s.pool.Exec(ctx, `UPDATE api_tokens SET last_used_at = $2, last_used_ip = $3 WHERE id = $1`, id, at, ip)
	return err
}

// DeleteAPITokensBefore removes tokens revoked or expired before cutoff.
func (s *Store) DeleteAPITokensBefore(ctx context.Context, cutoff time.Time) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM api_tokens WHERE revoked_at < $1 OR expires_at < $1`, cutoff)
	return err
}
