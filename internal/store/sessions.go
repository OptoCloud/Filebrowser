package store

import (
	"context"
	"time"
)

type Session struct {
	IDHash     []byte    `json:"-"`
	UserID     int64     `json:"user_id"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	IP         string    `json:"ip"`
	UserAgent  string    `json:"user_agent"`
	AuthMethod string    `json:"auth_method"`
	MFA        bool      `json:"mfa"`
}

const sessionCols = `id_hash, user_id, created_at, last_seen_at, expires_at, ip, user_agent, auth_method, mfa`

func (s *Store) CreateSession(ctx context.Context, sess *Session) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO sessions (`+sessionCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		sess.IDHash, sess.UserID, sess.CreatedAt, sess.LastSeenAt, sess.ExpiresAt, sess.IP, sess.UserAgent,
		sess.AuthMethod, sess.MFA)
	return err
}

func (s *Store) SessionByHash(ctx context.Context, hash []byte) (*Session, error) {
	return queryOne[Session](ctx, s, `SELECT `+sessionCols+` FROM sessions WHERE id_hash = $1`, hash)
}

func (s *Store) TouchSession(ctx context.Context, hash []byte, at time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE sessions SET last_seen_at = $2 WHERE id_hash = $1`, hash, at)
	return err
}

func (s *Store) DeleteSession(ctx context.Context, hash []byte) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE id_hash = $1`, hash)
	return err
}

// DeleteUserSession deletes a session only if it belongs to userID.
func (s *Store) DeleteUserSession(ctx context.Context, userID int64, hash []byte) error {
	return affected(s.pool.Exec(ctx, `DELETE FROM sessions WHERE id_hash = $1 AND user_id = $2`, hash, userID))
}

// DeleteUserSessions removes all sessions of a user except keep (may be nil).
func (s *Store) DeleteUserSessions(ctx context.Context, userID int64, keep []byte) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1 AND ($2::bytea IS NULL OR id_hash <> $2)`,
		userID, keep)
	return tag.RowsAffected(), err
}

func (s *Store) UserSessions(ctx context.Context, userID int64) ([]*Session, error) {
	return queryAll[Session](ctx, s, `SELECT `+sessionCols+` FROM sessions WHERE user_id = $1 ORDER BY last_seen_at DESC`, userID)
}

type SessionWithUser struct {
	Session
	Username string `json:"username"`
}

func (s *Store) AllSessions(ctx context.Context) ([]*SessionWithUser, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+prefixed("s.", sessionCols)+`, u.username
		FROM sessions s JOIN users u ON u.id = s.user_id ORDER BY s.last_seen_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SessionWithUser
	for rows.Next() {
		var x SessionWithUser
		if err := rows.Scan(&x.IDHash, &x.UserID, &x.CreatedAt, &x.LastSeenAt, &x.ExpiresAt, &x.IP,
			&x.UserAgent, &x.AuthMethod, &x.MFA, &x.Username); err != nil {
			return nil, err
		}
		out = append(out, &x)
	}
	return out, rows.Err()
}

func (s *Store) DeleteSessionsBefore(ctx context.Context, idleCutoff, now time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at < $2 OR last_seen_at < $1`, idleCutoff, now)
	return tag.RowsAffected(), err
}
