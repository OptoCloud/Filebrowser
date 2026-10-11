package store

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type User struct {
	ID           int64     `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	Role         string    `json:"role"`
	QuotaBytes   int64     `json:"quota_bytes"`
	TOTPSecret   []byte    `json:"-"`
	TOTPPending  []byte    `json:"-"`
	TOTPLastStep int64     `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
	Disabled     bool      `json:"disabled"`
	// MFAExempt lifts the two-factor requirement for a non-admin writer.
	MFAExempt bool `json:"mfa_exempt"`
}

func (u *User) IsAdmin() bool     { return u.Role == "admin" }
func (u *User) TOTPEnabled() bool { return len(u.TOTPSecret) > 0 }

type Grant struct {
	Path       string `json:"path"`
	Permission string `json:"permission"` // read or write
}

const userCols = `id, username, password_hash, role, quota_bytes, totp_secret, totp_pending, totp_last_step, created_at, disabled, mfa_exempt`

func scanUser(row pgx.Row) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.QuotaBytes,
		&u.TOTPSecret, &u.TOTPPending, &u.TOTPLastStep, &u.CreatedAt, &u.Disabled, &u.MFAExempt)
	if err != nil {
		return nil, notFound(err)
	}
	return &u, nil
}

func (s *Store) CreateUser(ctx context.Context, username, passwordHash, role string, quota int64) (*User, error) {
	u, err := scanUser(s.pool.QueryRow(ctx,
		`INSERT INTO users (username, password_hash, role, quota_bytes) VALUES ($1, $2, $3, $4) RETURNING `+userCols,
		username, passwordHash, role, quota))
	if isUniqueViolation(err) {
		return nil, ErrConflict
	}
	return u, err
}

func (s *Store) UserByID(ctx context.Context, id int64) (*User, error) {
	return scanUser(s.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id = $1`, id))
}

func (s *Store) UserByName(ctx context.Context, username string) (*User, error) {
	return scanUser(s.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE lower(username) = lower($1)`, username))
}

func (s *Store) ListUsers(ctx context.Context) ([]*User, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+userCols+` FROM users ORDER BY lower(username)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) CountAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE role = 'admin' AND NOT disabled`).Scan(&n)
	return n, err
}

type UserUpdate struct {
	Role       *string
	QuotaBytes *int64
	Disabled   *bool
	MFAExempt  *bool
}

func (s *Store) UpdateUser(ctx context.Context, id int64, up UserUpdate) error {
	return affected(s.pool.Exec(ctx, `UPDATE users SET
		role = COALESCE($2, role),
		quota_bytes = COALESCE($3, quota_bytes),
		disabled = COALESCE($4, disabled),
		mfa_exempt = COALESCE($5, mfa_exempt)
		WHERE id = $1`, id, up.Role, up.QuotaBytes, up.Disabled, up.MFAExempt))
}

func (s *Store) SetPassword(ctx context.Context, id int64, hash string) error {
	return affected(s.pool.Exec(ctx, `UPDATE users SET password_hash = $2 WHERE id = $1`, id, hash))
}

func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	return affected(s.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, id))
}

func (s *Store) SetTOTPPending(ctx context.Context, id int64, enc []byte) error {
	return affected(s.pool.Exec(ctx, `UPDATE users SET totp_pending = $2 WHERE id = $1`, id, enc))
}

// EnableTOTP promotes the pending secret. lastStep records the code used to confirm.
func (s *Store) EnableTOTP(ctx context.Context, id int64, enc []byte, lastStep int64) error {
	return affected(s.pool.Exec(ctx,
		`UPDATE users SET totp_secret = $2, totp_pending = NULL, totp_last_step = $3 WHERE id = $1`, id, enc, lastStep))
}

func (s *Store) DisableTOTP(ctx context.Context, id int64) error {
	return affected(s.pool.Exec(ctx,
		`UPDATE users SET totp_secret = NULL, totp_pending = NULL, totp_last_step = 0 WHERE id = $1`, id))
}

// ConsumeTOTPStep records a used time step, failing if it (or a later one) was
// already used. This prevents code replay.
func (s *Store) ConsumeTOTPStep(ctx context.Context, id, step int64) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE users SET totp_last_step = $2 WHERE id = $1 AND totp_last_step < $2`, id, step)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) Grants(ctx context.Context, userID int64) ([]Grant, error) {
	rows, err := s.pool.Query(ctx, `SELECT path, permission FROM grants WHERE user_id = $1 ORDER BY path`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Grant])
}

func (s *Store) SetGrants(ctx context.Context, userID int64, grants []Grant) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM grants WHERE user_id = $1`, userID); err != nil {
			return err
		}
		for _, g := range grants {
			if _, err := tx.Exec(ctx,
				`INSERT INTO grants (user_id, path, permission) VALUES ($1, $2, $3)
				 ON CONFLICT (user_id, path) DO UPDATE SET permission = excluded.permission`,
				userID, g.Path, g.Permission); err != nil {
				return err
			}
		}
		return nil
	})
}

type OIDCIdentity struct {
	Issuer    string    `json:"issuer"`
	Subject   string    `json:"subject"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Store) UserByOIDC(ctx context.Context, issuer, subject string) (*User, error) {
	return scanUser(s.pool.QueryRow(ctx, `SELECT `+prefixed("u.", userCols)+` FROM users u
		JOIN oidc_identities o ON o.user_id = u.id WHERE o.issuer = $1 AND o.subject = $2`, issuer, subject))
}

func (s *Store) LinkOIDC(ctx context.Context, userID int64, issuer, subject string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO oidc_identities (user_id, issuer, subject) VALUES ($1, $2, $3)`,
		userID, issuer, subject)
	if isUniqueViolation(err) {
		return ErrConflict
	}
	return err
}

// UnlinkOIDC removes a linked identity. It returns ErrLastLogin instead if
// that would leave the user with no way to log in (no password and no other
// linked identity).
func (s *Store) UnlinkOIDC(ctx context.Context, userID int64, issuer, subject string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Lock the user row so concurrent unlinks cannot both pass the check.
		var hasPassword bool
		if err := tx.QueryRow(ctx, `SELECT password_hash <> '' FROM users WHERE id = $1 FOR UPDATE`, userID).
			Scan(&hasPassword); err != nil {
			return notFound(err)
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM oidc_identities WHERE user_id = $1`, userID).Scan(&n); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM oidc_identities WHERE user_id = $1 AND issuer = $2 AND subject = $3`,
			userID, issuer, subject)
		if err := affected(tag, err); err != nil {
			return err
		}
		if !hasPassword && n <= 1 {
			return ErrLastLogin // rolls the delete back
		}
		return nil
	})
}

func (s *Store) OIDCIdentities(ctx context.Context, userID int64) ([]OIDCIdentity, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT issuer, subject, created_at FROM oidc_identities WHERE user_id = $1 ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[OIDCIdentity])
}

func prefixed(prefix, cols string) string {
	parts := strings.Split(cols, ",")
	for i, c := range parts {
		parts[i] = prefix + strings.TrimSpace(c)
	}
	return strings.Join(parts, ", ")
}
