package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"filebrowser/internal/auth"
	"filebrowser/internal/store"
)

// Regression tests for findings from the security scan.

// F1: a relative symlink that stays inside files_root must not carry a user
// (or a share) across grant boundaries.
func TestInRootSymlinkDoesNotCrossGrants(t *testing.T) {
	h := newHarness(t)
	write(t, h.root, "private/secret.txt", "TOP SECRET")
	write(t, h.root, "pub/ok.txt", "ok")
	if err := os.Symlink(filepath.Join("..", "private"), filepath.Join(h.root, "pub", "link")); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	if err := os.Symlink(filepath.Join("..", "private", "secret.txt"), filepath.Join(h.root, "pub", "file-link.txt")); err != nil {
		t.Fatal(err)
	}
	h.user("sym", "user", store.Grant{Path: "pub", Permission: "write"})
	c := h.login("sym")
	for _, u := range []string{
		"/api/files/download?path=pub/link/secret.txt", "/api/files/download?path=pub/file-link.txt",
		"/api/files?path=pub/link", "/api/thumb?path=pub/file-link.txt",
	} {
		if rec := c.do("GET", u, nil); rec.Code == 200 {
			t.Errorf("%s: 200 %q", u, rec.Body.String())
		}
	}
	list := c.do("GET", "/api/files?path=pub", nil)
	if strings.Contains(list.Body.String(), "link") {
		t.Errorf("listing shows links to other grants: %s", list.Body.String())
	}
	rec := c.do("GET", "/api/files/zip?paths=pub", nil)
	if strings.Contains(rec.Body.String(), "TOP SECRET") {
		t.Error("zip followed a link out of the grant")
	}
	if rec := c.do("POST", "/api/shares", map[string]any{"path": "pub/link"}); rec.Code == 200 {
		t.Error("shared a symlinked path outside the grant")
	}
	if rec := c.do("POST", "/api/files/mkdir", map[string]string{"path": "pub/link", "name": "x"}); rec.Code == 200 {
		t.Error("created a folder through a link outside the grant")
	}

	// A share of pub must not serve the target of an in-share symlink.
	created := decode[map[string]any](t, c.do("POST", "/api/shares", map[string]any{"path": "pub"}))
	token := strings.TrimPrefix(created["url"].(string), shareOrigin+"/s/")
	s := h.share()
	for _, u := range []string{"/s/" + token + "/file?path=file-link.txt", "/s/" + token + "/file?path=link/secret.txt", "/s/" + token + "/api/list?path=link"} {
		if rec := s.do("GET", u, nil); rec.Code == 200 {
			t.Errorf("share %s: 200", u)
		}
	}
	if rec := s.do("GET", "/s/"+token+"/zip", nil); strings.Contains(rec.Body.String(), "TOP SECRET") {
		t.Error("share zip followed a link out of the share")
	}
}

// F2: subtitle sidecars must be authorized on their own path.
func TestSubtitleSidecarOutsideGrant(t *testing.T) {
	h := newHarness(t)
	write(t, h.root, "vault/pub/movie.mkv", "x")
	write(t, h.root, "vault/secret.srt", "1\n00:00:00,000 --> 00:00:01,000\nCANARY\n")
	h.user("subs", "user", store.Grant{Path: "vault/pub", Permission: "read"})
	c := h.login("subs")
	for _, u := range []string{
		"/api/media/subtitle?path=vault/pub&sidecar=secret.srt",
		"/api/media/subtitle?path=vault/pub/movie.mkv&sidecar=..%2Fsecret.srt",
	} {
		rec := c.do("GET", u, nil)
		if rec.Code == 200 || strings.Contains(rec.Body.String(), "CANARY") {
			t.Errorf("%s: %d %q", u, rec.Code, rec.Body.String())
		}
	}
}

// F3: Range requests must not bypass share download limits.
func TestShareLimitRangeBypass(t *testing.T) {
	h := newHarness(t)
	h.user("lim", "user", store.Grant{Path: "lim", Permission: "write"})
	write(t, h.root, "lim/f.bin", "0123456789")
	c := h.login("lim")
	created := decode[map[string]any](t, c.do("POST", "/api/shares", map[string]any{"path": "lim/f.bin", "max_downloads": 1}))
	token := strings.TrimPrefix(created["url"].(string), shareOrigin+"/s/")
	first := h.share()
	expect(t, first.do("GET", "/s/"+token+"/file", nil), 200)
	first.headers["Range"] = "bytes=5-"
	expect(t, first.do("GET", "/s/"+token+"/file", nil), 206) // the same browser may resume
	for _, rng := range []string{"bytes=1-", "bytes=-100", "bytes=0-"} {
		other := h.share()
		other.headers["Range"] = rng
		if rec := other.do("GET", "/s/"+token+"/file", nil); rec.Code != http.StatusGone {
			t.Errorf("Range %s from a new browser: %d", rng, rec.Code)
		}
	}
}

// F4: failures from one IP must not lock the account owner out for long.
func TestNoAccountLockout(t *testing.T) {
	h := newHarness(t)
	h.user("victim", "user")
	attacker := h.app()
	attacker.ip = "203.0.113.9"
	for i := range 30 {
		attacker.do("POST", "/api/auth/login", map[string]string{"username": "victim", "password": "wrong password " + strconv.Itoa(i)})
	}
	owner := h.app()
	owner.ip = "198.51.100.7"
	rec := owner.do("POST", "/api/auth/login", map[string]string{"username": "victim", "password": testPassword})
	if rec.Code == http.StatusTooManyRequests {
		if ra, _ := strconv.Atoi(rec.Header().Get("Retry-After")); ra > 31 {
			t.Fatalf("owner locked out for %ds", ra)
		}
	} else {
		expect(t, rec, 200)
	}
	// The attacking IP stays throttled.
	expect(t, attacker.do("POST", "/api/auth/login", map[string]string{"username": "victim", "password": testPassword}), 429)
}

// F14: a first password needs a fresh login (or TOTP), not just a session.
func TestFirstPasswordNeedsReauth(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	u, err := h.store.CreateUser(ctx, "oidconly", "", "user", 0)
	if err != nil {
		t.Fatal(err)
	}
	rec := h.session(u, time.Hour).do("POST", "/api/me/password", map[string]string{"new": testPassword})
	expect(t, rec, 401)
	if decode[map[string]string](t, rec)["code"] != "reauth_required" {
		t.Fatalf("body %s", rec.Body.String())
	}
	expect(t, h.session(u, time.Minute).do("POST", "/api/me/password", map[string]string{"new": testPassword}), 200)
}

// F16: malformed audit filters are rejected, not passed to the database.
func TestAuditFilterValidation(t *testing.T) {
	h := newHarness(t)
	h.user("adm", "admin")
	a := h.login("adm")
	expect(t, a.do("GET", "/api/admin/audit?action=%00", nil), 400)
	expect(t, a.do("GET", "/api/admin/audit?action=%25", nil), 400)
	expect(t, a.do("GET", "/api/admin/audit?action=auth.", nil), 200)
}

// F13/F15: signed links are never cached and carry CORP.
func TestSignedLinkHeaders(t *testing.T) {
	h := newHarness(t)
	h.user("adm", "admin")
	write(t, h.root, "doc.pdf", "%PDF-1.4")
	a := h.login("adm")
	link := decode[map[string]string](t, a.do("POST", "/api/files/preview-url", map[string]string{"path": "doc.pdf"}))["url"]
	rec := h.share().do("GET", strings.TrimPrefix(link, shareOrigin), nil)
	expect(t, rec, 200)
	if rec.Header().Get("Cache-Control") != "private, no-store" || rec.Header().Get("Cross-Origin-Resource-Policy") == "" {
		t.Errorf("headers %v", rec.Header())
	}
}

// session logs u in directly, as an OIDC login created age ago.
func (h *harness) session(u *store.User, age time.Duration) *client {
	h.t.Helper()
	token := auth.NewToken(32)
	now := time.Now()
	err := h.store.CreateSession(context.Background(), &store.Session{IDHash: h.srv.keys.HashToken(token), UserID: u.ID,
		CreatedAt: now.Add(-age), LastSeenAt: now, ExpiresAt: now.Add(time.Hour), AuthMethod: "oidc", MFA: true})
	if err != nil {
		h.t.Fatal(err)
	}
	c := h.app()
	c.cookies["fb_session"] = &http.Cookie{Name: "fb_session", Value: token}
	return c
}

// An account without a password cannot unlink its last login provider.
func TestOIDCUnlinkKeepsALoginMethod(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	u, err := h.store.CreateUser(ctx, "oidconly", "", "user", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"a", "b"} {
		if err := h.store.LinkOIDC(ctx, u.ID, "https://idp", sub); err != nil {
			t.Fatal(err)
		}
	}
	c := h.session(u, time.Minute)
	expect(t, c.do("POST", "/api/me/oidc/unlink", map[string]string{"issuer": "https://idp", "subject": "a"}), 200)
	rec := c.do("POST", "/api/me/oidc/unlink", map[string]string{"issuer": "https://idp", "subject": "b"})
	expect(t, rec, 409)
	if ids, _ := h.store.OIDCIdentities(ctx, u.ID); len(ids) != 1 {
		t.Fatalf("identities left: %v", ids)
	}
	expect(t, c.do("POST", "/api/me/password", map[string]string{"new": testPassword}), 200)
	c = h.login("oidconly")
	expect(t, c.do("POST", "/api/me/oidc/unlink", map[string]string{"issuer": "https://idp", "subject": "b"}), 200)
}

// Disabling TOTP is throttled like any other password or code check.
func TestTOTPDisableIsThrottled(t *testing.T) {
	h := newHarness(t)
	h.user("reader", "user", store.Grant{Path: "pub", Permission: "read"})
	c := h.login("reader")
	setup := decode[map[string]string](t, c.do("POST", "/api/auth/totp/setup", struct{}{}))
	secret, err := base32Decode(setup["secret"])
	if err != nil {
		t.Fatal(err)
	}
	expect(t, c.do("POST", "/api/auth/totp/enable", map[string]string{"code": auth.TOTPCode(secret, time.Now())}), 200)
	var last int
	for i := range 8 {
		last = c.do("POST", "/api/auth/totp/disable", map[string]string{"password": "wrong password " + strconv.Itoa(i), "code": "000000"}).Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("status after repeated failures = %d, want 429", last)
	}
}

// Fixes from the CVE review (2026-10).

// A locked share reveals nothing; weak passwords are refused; guessing is
// limited per share as well as per client.
func TestShareLockAndPasswordRules(t *testing.T) {
	h := newHarness(t)
	h.user("eve", "user", store.Grant{Path: "eve", Permission: "write"})
	write(t, h.root, "eve/secret-plans.pdf", "%PDF")
	c := h.login("eve")
	rec := c.do("POST", "/api/shares", map[string]any{"path": "eve/secret-plans.pdf", "password": "1234"})
	expect(t, rec, 400)
	if !strings.Contains(rec.Body.String(), "8 to 256 characters") {
		t.Errorf("weak password error: %s", rec.Body.String())
	}
	created := decode[map[string]any](t, c.do("POST", "/api/shares", map[string]any{"path": "eve/secret-plans.pdf", "password": "long enough pw"}))
	token := strings.TrimPrefix(created["url"].(string), shareOrigin+"/s/")
	info := h.share().do("GET", "/s/"+token+"/api/info", nil)
	if strings.Contains(info.Body.String(), "secret-plans") || strings.Contains(info.Body.String(), "expires") {
		t.Errorf("locked share info leaks metadata: %s", info.Body.String())
	}
	// Guesses from many clients hit the per-share limit.
	var last int
	for i := range 14 {
		s := h.share()
		s.ip = "198.51.100." + strconv.Itoa(i+1)
		last = s.do("POST", "/s/"+token+"/api/unlock", map[string]string{"password": "guess " + strconv.Itoa(i)}).Code
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("distributed guessing not limited per share: last status %d", last)
	}
}

// A share stops working when its file is replaced by another one at the same path.
func TestShareBoundToFileIdentity(t *testing.T) {
	h := newHarness(t)
	h.user("fay", "user", store.Grant{Path: "fay", Permission: "write"})
	write(t, h.root, "fay/report.txt", "original")
	c := h.login("fay")
	created := decode[map[string]any](t, c.do("POST", "/api/shares", map[string]any{"path": "fay/report.txt"}))
	token := strings.TrimPrefix(created["url"].(string), shareOrigin+"/s/")
	expect(t, h.share().do("GET", "/s/"+token+"/api/info", nil), 200)
	expect(t, c.do("POST", "/api/files/delete", map[string]any{"paths": []string{"fay/report.txt"}}), 200)
	write(t, h.root, "fay/report.txt", "something else entirely")
	rec := h.share().do("GET", "/s/"+token+"/file", nil)
	if rec.Code == 200 || strings.Contains(rec.Body.String(), "something else") {
		t.Fatalf("share served the replacement file: %d %q", rec.Code, rec.Body.String())
	}
}

// Signed links die with the session that made them.
func TestSignedLinkRevokedByLogout(t *testing.T) {
	h := newHarness(t)
	h.user("gus", "admin")
	write(t, h.root, "doc.pdf", "%PDF-1.4")
	a := h.login("gus")
	link := decode[map[string]string](t, a.do("POST", "/api/files/preview-url", map[string]string{"path": "doc.pdf"}))["url"]
	path := strings.TrimPrefix(link, shareOrigin)
	expect(t, h.share().do("GET", path, nil), 200)
	expect(t, a.do("POST", "/api/auth/logout", struct{}{}), 200)
	expect(t, h.share().do("GET", path, nil), 404)
}

// Trashed files count towards the quota until purged.
func TestTrashCountsTowardsQuota(t *testing.T) {
	h := newHarness(t)
	u := h.user("hal", "user", store.Grant{Path: "hal", Permission: "read"})
	q := int64(100)
	if err := h.store.UpdateUser(context.Background(), u.ID, store.UserUpdate{QuotaBytes: &q}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.SetGrants(context.Background(), u.ID, []store.Grant{{Path: "hal", Permission: "write"}}); err != nil {
		t.Fatal(err)
	}
	write(t, h.root, "hal/big.bin", strings.Repeat("x", 80))
	c := h.login("hal")
	expect(t, c.do("POST", "/api/files/delete", map[string]any{"paths": []string{"hal/big.bin"}}), 200)
	rec, _ := c.tusUpload("/api/uploads/", "hal", "again.bin", strings.Repeat("y", 50))
	expect(t, rec, http.StatusRequestEntityTooLarge)
	expect(t, c.do("POST", "/api/trash/purge", map[string]any{"all": true}), 200)
	rec, _ = c.tusUpload("/api/uploads/", "hal", "again.bin", strings.Repeat("y", 50))
	if rec.Code >= 300 {
		t.Fatalf("upload after purging trash: %d %s", rec.Code, rec.Body.String())
	}
}

// NUL characters in JSON are a client error, not a database error.
func TestJSONRejectsNUL(t *testing.T) {
	h := newHarness(t)
	h.user("ivy", "admin")
	a := h.login("ivy")
	expect(t, a.do("POST", "/api/trash/restore", json.RawMessage(`{"ids":["a\u0000b"]}`)), 400)
	expect(t, h.app().do("POST", "/api/auth/login", json.RawMessage(`{"username":"ivy\u0000","password":"x"}`)), 400)
}
