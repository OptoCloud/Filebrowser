package store

import (
	"context"
	"time"
)

// Webhook is an address called when something happens to a user's files
// (see migration 0009).
type Webhook struct {
	ID         int64      `json:"id"`
	UserID     int64      `json:"user_id"`
	Name       string     `json:"name"`
	URL        string     `json:"url"`
	Secret     []byte     `json:"-"`
	Events     []string   `json:"events"`
	Path       string     `json:"path"`
	CreatedAt  time.Time  `json:"created_at"`
	Disabled   bool       `json:"disabled"`
	LastAt     *time.Time `json:"last_at"`
	LastStatus int        `json:"last_status"`
	LastError  string     `json:"last_error"`
	Failures   int        `json:"failures"`
}

const webhookCols = `id, user_id, name, url, secret, events, path, created_at, disabled, last_at, last_status, last_error, failures`

func (s *Store) CreateWebhook(ctx context.Context, h *Webhook) error {
	return s.pool.QueryRow(ctx, `INSERT INTO webhooks (user_id, name, url, secret, events, path)
		VALUES ($1,$2,$3,$4,$5,$6) RETURNING id, created_at`,
		h.UserID, h.Name, h.URL, h.Secret, append([]string{}, h.Events...), h.Path).Scan(&h.ID, &h.CreatedAt)
}

// Webhooks lists a user's webhooks, oldest first.
func (s *Store) Webhooks(ctx context.Context, userID int64) ([]*Webhook, error) {
	return queryAll[Webhook](ctx, s, `SELECT `+webhookCols+` FROM webhooks WHERE user_id = $1 ORDER BY id`, userID)
}

// WebhooksFor lists the enabled webhooks of a user that take event.
func (s *Store) WebhooksFor(ctx context.Context, userID int64, event string) ([]*Webhook, error) {
	return queryAll[Webhook](ctx, s, `SELECT `+webhookCols+` FROM webhooks
		WHERE user_id = $1 AND NOT disabled AND $2 = ANY(events) ORDER BY id`, userID, event)
}

func (s *Store) Webhook(ctx context.Context, userID, id int64) (*Webhook, error) {
	return queryOne[Webhook](ctx, s, `SELECT `+webhookCols+` FROM webhooks WHERE id = $2 AND user_id = $1`, userID, id)
}

func (s *Store) DeleteWebhook(ctx context.Context, userID, id int64) error {
	return affected(s.pool.Exec(ctx, `DELETE FROM webhooks WHERE id = $2 AND user_id = $1`, userID, id))
}

// RecordWebhook notes the outcome of a delivery. A hook that has failed
// maxFailures times in a row is disabled.
func (s *Store) RecordWebhook(ctx context.Context, id int64, at time.Time, status int, errText string, ok bool, maxFailures int) error {
	_, err := s.pool.Exec(ctx, `UPDATE webhooks SET last_at = $2, last_status = $3, last_error = $4,
		failures = CASE WHEN $5 THEN 0 ELSE failures + 1 END,
		disabled = disabled OR (NOT $5 AND failures + 1 >= $6::int)
		WHERE id = $1`, id, at, status, errText, ok, maxFailures)
	return err
}
