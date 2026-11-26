// Package webhooks calls the addresses users have registered when something
// happens to their files.
package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"syscall"
	"time"

	"filebrowser/internal/auth"
	"filebrowser/internal/files"
	"filebrowser/internal/store"
)

// Events a webhook can take.
const (
	FileUploaded  = "file.uploaded"  // the user (or a token of theirs) uploaded a file
	DropReceived  = "drop.received"  // someone uploaded through one of the user's drop links
	ShareCreated  = "share.created"  // a share link was made
	ShareAccessed = "share.accessed" // a share link was used for a download
	Ping          = "ping"           // sent by the "test" button
)

var Events = []string{FileUploaded, DropReceived, ShareCreated, ShareAccessed}

const (
	// MaxPerUser bounds how many hooks a user can have.
	MaxPerUser = 10
	// maxFailures in a row disable a hook.
	maxFailures = 20
	timeout     = 10 * time.Second
)

// retryAfter are the waits before the second and third attempt.
var retryAfter = []time.Duration{5 * time.Second, 30 * time.Second}

type delivery struct {
	hook    *store.Webhook
	private bool
	body    []byte
	event   string
	id      string
}

// Dispatcher delivers events. Emit never blocks: deliveries wait in a
// bounded queue for a few workers, and are dropped (and logged) when it is
// full rather than hold up the request that caused them.
type Dispatcher struct {
	Store *store.Store
	Keys  *auth.Keys

	queue  chan delivery
	client *http.Client
}

type privateKey struct{}

func New(st *store.Store, keys *auth.Keys) *Dispatcher {
	d := &Dispatcher{Store: st, Keys: keys, queue: make(chan delivery, 1000)}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	// The check is on the address being connected to, after the name was
	// resolved, so a name cannot be pointed somewhere else between a check
	// and the connection.
	dialer.ControlContext = func(ctx context.Context, _, address string, _ syscall.RawConn) error {
		private, _ := ctx.Value(privateKey{}).(bool)
		return CheckAddress(address, private)
	}
	d.client = &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: dialer.DialContext, Proxy: nil, MaxIdleConns: 10, IdleConnTimeout: time.Minute,
			TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: timeout,
		},
		// A redirect could lead where the address check would not have let the hook point.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return d
}

// CheckAddress reports whether a webhook may connect to address (ip:port).
// Only an administrator's hooks may reach private, loopback and link-local
// addresses: those are the server's own network, which other users must not
// be able to make it call into.
func CheckAddress(address string, private bool) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return err
	}
	a := ap.Addr().Unmap()
	if a.IsUnspecified() || a.IsMulticast() {
		return errors.New("address not allowed")
	}
	if private {
		return nil
	}
	cgnat := netip.MustParsePrefix("100.64.0.0/10")
	if a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || cgnat.Contains(a) {
		return errors.New("webhooks to private addresses are for administrators")
	}
	return nil
}

// CheckURL validates the address of a new webhook.
func CheckURL(raw string, private bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || len(raw) > 2000 {
		return errors.New("a webhook address is an http(s) URL")
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !private) {
		return errors.New("a webhook address must use https")
	}
	// A literal address is checked now; a name when it is connected to.
	if a, err := netip.ParseAddr(u.Hostname()); err == nil {
		return CheckAddress(netip.AddrPortFrom(a, 443).String(), private)
	}
	return nil
}

// Run delivers queued events until ctx is done.
func (d *Dispatcher) Run(ctx context.Context) {
	for range 2 {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case dl := <-d.queue:
					d.deliver(ctx, dl)
				}
			}
		}()
	}
}

// Emit queues event for the hooks of userID that take it and whose path the
// file is under. path is relative to files_root; data is event-specific.
func (d *Dispatcher) Emit(ctx context.Context, userID int64, event, path string, data any) {
	ctx = context.WithoutCancel(ctx)
	hooks, err := d.Store.WebhooksFor(ctx, userID, event)
	if err != nil || len(hooks) == 0 {
		if err != nil {
			slog.Error("webhooks", "err", err)
		}
		return
	}
	user, err := d.Store.UserByID(ctx, userID)
	if err != nil || user.Disabled {
		return
	}
	for _, h := range hooks {
		if !files.Within(path, h.Path) {
			continue
		}
		d.enqueue(h, user.IsAdmin(), event, path, data)
	}
}

// Test queues a ping for one hook.
func (d *Dispatcher) Test(h *store.Webhook, admin bool) { d.enqueue(h, admin, Ping, "", nil) }

func (d *Dispatcher) enqueue(h *store.Webhook, admin bool, event, path string, data any) {
	id := auth.NewToken(12)
	body, err := json.Marshal(map[string]any{"id": id, "event": event, "time": time.Now().UTC().Format(time.RFC3339),
		"path": path, "data": data})
	if err != nil {
		return
	}
	select {
	case d.queue <- delivery{hook: h, private: admin, body: body, event: event, id: id}:
	default:
		slog.Warn("webhook queue full, event dropped", "webhook", h.ID, "event", event)
	}
}

func (d *Dispatcher) deliver(ctx context.Context, dl delivery) {
	secret, err := d.Keys.Decrypt(dl.hook.Secret, []byte("webhook"))
	if err != nil {
		slog.Error("webhook secret", "webhook", dl.hook.ID, "err", err)
		return
	}
	var status int
	for attempt := 0; ; attempt++ {
		status, err = d.post(ctx, dl, secret)
		if err == nil || attempt >= len(retryAfter) {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(retryAfter[attempt]):
		}
	}
	errText := ""
	if err != nil {
		errText = err.Error()
		if len(errText) > 300 {
			errText = errText[:300]
		}
	}
	if rerr := d.Store.RecordWebhook(context.WithoutCancel(ctx), dl.hook.ID, time.Now(), status, errText, err == nil, maxFailures); rerr != nil {
		slog.Error("webhook record", "err", rerr)
	}
}

// Sign is the signature of a delivery: HMAC-SHA256 over "<timestamp>.<body>"
// with the hook's secret, so a receiver can tell the call is the server's
// and how old it is.
func Sign(secret []byte, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(timestamp + "."))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func (d *Dispatcher) post(ctx context.Context, dl delivery, secret []byte) (int, error) {
	ctx = context.WithValue(ctx, privateKey{}, dl.private)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, dl.hook.URL, bytes.NewReader(dl.body))
	if err != nil {
		return 0, err
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "filebrowser-webhook")
	req.Header.Set("X-Fb-Event", dl.event)
	req.Header.Set("X-Fb-Delivery", dl.id)
	req.Header.Set("X-Fb-Timestamp", ts)
	req.Header.Set("X-Fb-Signature", Sign(secret, ts, dl.body))
	res, err := d.client.Do(req)
	if err != nil {
		// Without the address: it is the hook's own, and may hold a secret of the receiver.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return 0, err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4<<10))
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return res.StatusCode, fmt.Errorf("the receiver answered %d", res.StatusCode)
	}
	return res.StatusCode, nil
}
