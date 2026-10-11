package store

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

// WebAuthnCredential is a registered security key or passkey. Credential
// holds the webauthn library's JSON (public key, flags, sign count).
type WebAuthnCredential struct {
	ID         []byte          `json:"-"`
	UserID     int64           `json:"-"`
	Name       string          `json:"name"`
	Credential json.RawMessage `json:"-"`
	Passkey    bool            `json:"passkey"`
	CreatedAt  time.Time       `json:"created_at"`
	LastUsedAt *time.Time      `json:"last_used_at"`
}

const webauthnCols = `id, user_id, name, credential, passkey, created_at, last_used_at`

// WebAuthnHandle returns the user's WebAuthn user handle, creating it on first use.
func (s *Store) WebAuthnHandle(ctx context.Context, userID int64) ([]byte, error) {
	var h []byte
	if err := s.pool.QueryRow(ctx, `SELECT webauthn_handle FROM users WHERE id = $1`, userID).Scan(&h); err != nil {
		return nil, notFound(err)
	}
	if h != nil {
		return h, nil
	}
	fresh := make([]byte, 32)
	if _, err := rand.Read(fresh); err != nil {
		return nil, err
	}
	err := s.pool.QueryRow(ctx, `UPDATE users SET webauthn_handle = COALESCE(webauthn_handle, $2)
		WHERE id = $1 RETURNING webauthn_handle`, userID, fresh).Scan(&h)
	return h, notFound(err)
}

func (s *Store) UserByWebAuthnHandle(ctx context.Context, handle []byte) (*User, error) {
	return scanUser(s.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE webauthn_handle = $1`, handle))
}

func (s *Store) WebAuthnCredentials(ctx context.Context, userID int64) ([]*WebAuthnCredential, error) {
	return queryAll[WebAuthnCredential](ctx, s, `SELECT `+webauthnCols+` FROM webauthn_credentials
		WHERE user_id = $1 ORDER BY created_at`, userID)
}

// CountWebAuthn is the number of keys each user has registered.
func (s *Store) CountWebAuthn(ctx context.Context) (map[int64]int, error) {
	rows, err := s.pool.Query(ctx, `SELECT user_id, count(*) FROM webauthn_credentials GROUP BY user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var uid int64
		var n int
		if err := rows.Scan(&uid, &n); err != nil {
			return nil, err
		}
		out[uid] = n
	}
	return out, rows.Err()
}

func (s *Store) AddWebAuthnCredential(ctx context.Context, c *WebAuthnCredential) error {
	err := s.pool.QueryRow(ctx, `INSERT INTO webauthn_credentials (id, user_id, name, credential, passkey)
		VALUES ($1,$2,$3,$4,$5) RETURNING created_at`, c.ID, c.UserID, c.Name, c.Credential, c.Passkey).Scan(&c.CreatedAt)
	if isUniqueViolation(err) {
		return ErrConflict
	}
	return err
}

// UseWebAuthnCredential stores the credential's updated state (sign count,
// flags) after a login.
func (s *Store) UseWebAuthnCredential(ctx context.Context, id []byte, credential json.RawMessage, at time.Time) error {
	return affected(s.pool.Exec(ctx, `UPDATE webauthn_credentials SET credential = $2, last_used_at = $3 WHERE id = $1`,
		id, credential, at))
}

// DeleteWebAuthnCredential removes one of the user's keys. It returns
// ErrLastLogin instead if keepOne is set and it is the user's last second
// factor (no TOTP and no other key).
func (s *Store) DeleteWebAuthnCredential(ctx context.Context, userID int64, id []byte, keepOne bool) error {
	return pgx.BeginFunc(ctx, s.pool, func(q pgx.Tx) error {
		var hasTOTP bool
		if err := q.QueryRow(ctx, `SELECT totp_secret IS NOT NULL FROM users WHERE id = $1 FOR UPDATE`, userID).
			Scan(&hasTOTP); err != nil {
			return notFound(err)
		}
		var n int
		if err := q.QueryRow(ctx, `SELECT count(*) FROM webauthn_credentials WHERE user_id = $1`, userID).Scan(&n); err != nil {
			return err
		}
		tag, err := q.Exec(ctx, `DELETE FROM webauthn_credentials WHERE user_id = $1 AND id = $2`, userID, id)
		if err := affected(tag, err); err != nil {
			return err
		}
		if keepOne && !hasTOTP && n <= 1 {
			return ErrLastLogin // rolls the delete back
		}
		return nil
	})
}

// ResetSecondFactors removes the user's TOTP and all security keys and
// passkeys (account recovery by an admin or the CLI).
func (s *Store) ResetSecondFactors(ctx context.Context, userID int64) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE users SET totp_secret = NULL, totp_pending = NULL, totp_last_step = 0 WHERE id = $1`, userID)
		if err := affected(tag, err); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM webauthn_credentials WHERE user_id = $1`, userID)
		return err
	})
}
