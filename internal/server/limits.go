package server

import (
	"time"

	"filebrowser/internal/auth"
)

// The server's rate limits and retention times, in one place.

// perMinute is a rate limit per client address: so many a minute, with a
// burst on top of what has not been used.
type perMinute struct{ rate, burst int }

func (p perMinute) limiter() *auth.RateLimiter { return auth.NewRateLimiter(p.rate, p.burst) }

var (
	// Lookups of share tokens, link names and signed links.
	shareLookups = perMinute{60, 120}
	// Uploads started through drop links: a folder of small files is many
	// in a short time.
	dropUploads = perMinute{600, 1200}
	// Requests with an API token that is not valid.
	badTokens = perMinute{30, 60}
	// Policy violation reports that are logged.
	policyReports = perMinute{10, 20}
	// Invite lookups, passkey logins and WebAuthn ceremonies started.
	inviteLookups = perMinute{10, 20}
	passkeyLogins = perMinute{30, 30}
	ceremonies    = perMinute{20, 20}
)

// backoff slows repeated failures down: free attempts, then a wait that
// doubles from base up to max; a key that has been quiet for backoffForget
// starts over.
type backoff struct {
	free      int
	base, max time.Duration
}

const backoffForget = time.Hour

func (b backoff) new() *auth.Backoff { return auth.NewBackoff(b.free, b.base, b.max, backoffForget) }

var (
	// Wrong passwords, per client address.
	loginFailures = backoff{5, time.Second, 15 * time.Minute}
	// Wrong passwords, per account: short, so that guessing from many
	// addresses is slowed without locking the owner out for long.
	accountFailures = backoff{20, time.Second, 30 * time.Second}
	// Wrong second factors, per account.
	totpFailures = backoff{5, 2 * time.Second, 15 * time.Minute}
	// Wrong share passwords, per share.
	sharePasswordFailures = backoff{10, time.Second, time.Minute}
)

const (
	// janitorEvery is how often expired things are swept.
	janitorEvery = time.Hour
	// Ended shares and API tokens are kept this long for their owner's reference.
	keepEndedDays = 30
	// What was learned of a file's type is forgotten after this long unused.
	keepFileTypesDays = 90
	// An upload that has stood still this long is given up.
	staleUpload = 24 * time.Hour
)
