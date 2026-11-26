package store

import (
	"context"
	"time"
)

// R2Upload is an upload through the R2 buffer (see migration 0010).
type R2Upload struct {
	ID        string
	Owner     string
	UserID    int64
	DestDir   string
	Name      string
	Size      int64
	PartSize  int64
	ObjectKey string
	UploadID  string
	State     string
	Parts     []byte
	Attempts  int
	Error     string
	FinalPath string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// States of an R2Upload. The first three hold quota and bucket space.
const (
	R2Uploading = "uploading"
	R2Uploaded  = "uploaded"
	R2Pulling   = "pulling"
	R2Done      = "done"
	R2Failed    = "failed"
	R2Expired   = "expired"
)

const r2Cols = `id, owner, user_id, dest_dir, name, size, part_size, object_key, upload_id, state, parts, attempts, error, final_path, created_at, updated_at`

func (s *Store) CreateR2Upload(ctx context.Context, u *R2Upload) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO r2_uploads (id, owner, user_id, dest_dir, name, size, part_size, object_key, upload_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, u.ID, u.Owner, u.UserID, u.DestDir, u.Name, u.Size, u.PartSize, u.ObjectKey, u.UploadID)
	return err
}

func (s *Store) R2Upload(ctx context.Context, id string) (*R2Upload, error) {
	return queryOne[R2Upload](ctx, s, `SELECT `+r2Cols+` FROM r2_uploads WHERE id = $1`, id)
}

// TouchR2Upload notes that the upload is being worked on.
func (s *Store) TouchR2Upload(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `UPDATE r2_uploads SET updated_at = now() WHERE id = $1`, id)
	return err
}

// MoveR2Upload takes an upload from one state to another, reporting
// whether it was in the first: two requests cannot both take the step.
func (s *Store) MoveR2Upload(ctx context.Context, id, from, to string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE r2_uploads SET state = $3, updated_at = now() WHERE id = $1 AND state = $2`, id, from, to)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// SetR2Parts records the parts of a completed upload.
func (s *Store) SetR2Parts(ctx context.Context, id string, parts []byte) error {
	_, err := s.pool.Exec(ctx, `UPDATE r2_uploads SET parts = $2 WHERE id = $1`, id, parts)
	return err
}

// FinishR2Upload ends an upload in state (done, failed or expired).
func (s *Store) FinishR2Upload(ctx context.Context, id, state, finalPath, errText string) error {
	_, err := s.pool.Exec(ctx, `UPDATE r2_uploads SET state = $2, final_path = $3, error = $4, updated_at = now() WHERE id = $1`,
		id, state, finalPath, errText)
	return err
}

// RetryR2Upload puts a pull that failed back in line, counting the attempt.
func (s *Store) RetryR2Upload(ctx context.Context, id, errText string) error {
	_, err := s.pool.Exec(ctx, `UPDATE r2_uploads SET state = 'uploaded', attempts = attempts + 1, error = $2, updated_at = now()
		WHERE id = $1`, id, errText)
	return err
}

// R2UploadsIn lists the uploads in a state, oldest first.
func (s *Store) R2UploadsIn(ctx context.Context, state string, limit int) ([]*R2Upload, error) {
	return queryAll[R2Upload](ctx, s, `SELECT `+r2Cols+` FROM r2_uploads WHERE state = $1 ORDER BY updated_at LIMIT $2`, state, limit)
}

// R2UploadsPending lists the uploads that hold quota and bucket space.
func (s *Store) R2UploadsPending(ctx context.Context) ([]*R2Upload, error) {
	return queryAll[R2Upload](ctx, s, `SELECT `+r2Cols+` FROM r2_uploads WHERE state IN ('uploading', 'uploaded', 'pulling')`)
}

// R2PendingBytes is how much the pending uploads may come to hold in the bucket.
func (s *Store) R2PendingBytes(ctx context.Context) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(SUM(size), 0)::bigint FROM r2_uploads WHERE state IN ('uploading', 'uploaded', 'pulling')`).Scan(&n)
	return n, err
}

// DeleteR2UploadsBefore forgets ended uploads older than cutoff.
func (s *Store) DeleteR2UploadsBefore(ctx context.Context, cutoff time.Time) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM r2_uploads WHERE state IN ('done', 'failed', 'expired') AND updated_at < $1`, cutoff)
	return err
}
