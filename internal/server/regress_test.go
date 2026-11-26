package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"filebrowser/internal/auth"
	"filebrowser/internal/files"
	"filebrowser/internal/store"
	"filebrowser/internal/uploads"
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

// A stolen session cookie must not be turned into a lasting credential:
// adding TOTP or a passkey needs a recent login.
func TestNewCredentialNeedsFreshLogin(t *testing.T) {
	h := newHarness(t)
	u := h.user("reader", "user")
	for _, ep := range []string{"/api/auth/totp/setup", "/api/me/webauthn/register/begin"} {
		rec := h.session(u, time.Hour).do("POST", ep, map[string]string{})
		expect(t, rec, 401)
		if decode[map[string]string](t, rec)["code"] != "reauth_required" {
			t.Fatalf("%s: body %s", ep, rec.Body.String())
		}
		expect(t, h.session(u, time.Minute).do("POST", ep, map[string]string{}), 200)
	}
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
	if !strings.Contains(rec.Body.String(), "10 to 256 characters") {
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

// Admins can exempt selected non-admin users from the two-factor requirement.
func TestMFAExemption(t *testing.T) {
	h := newHarness(t)
	h.user("root", "admin")
	admin := h.login("root")
	u := h.user("lou", "user", store.Grant{Path: "lou", Permission: "write"})
	write(t, h.root, "lou/a.txt", "a")
	restricted := func(c *client) any { return decode[map[string]any](t, c.do("GET", "/api/me", nil))["restricted"] }

	c := h.app()
	expect(t, c.do("POST", "/api/auth/login", map[string]string{"username": "lou", "password": testPassword}), 200)
	if restricted(c) != true {
		t.Fatal("a writer without a second factor should be restricted")
	}
	id := strconv.FormatInt(u.ID, 10)
	expect(t, admin.do("PATCH", "/api/admin/users/"+id, map[string]any{"mfa_exempt": true}), 200)
	if restricted(c) != false {
		t.Fatal("exemption did not lift the restriction")
	}
	expect(t, c.do("GET", "/api/files?path=lou", nil), 200)
	expect(t, admin.do("PATCH", "/api/admin/users/"+id, map[string]any{"mfa_exempt": false}), 200)
	if restricted(c) != true {
		t.Fatal("removing the exemption did not restore the requirement")
	}

	// Admins cannot be exempted, and promoting an exempt user ends the exemption.
	expect(t, admin.do("PATCH", "/api/admin/users/"+id, map[string]any{"mfa_exempt": true, "role": "admin"}), 400)
	expect(t, admin.do("PATCH", "/api/admin/users/"+id, map[string]any{"mfa_exempt": true}), 200)
	expect(t, admin.do("PATCH", "/api/admin/users/"+id, map[string]any{"role": "admin"}), 200)
	if got, _ := h.store.UserByID(context.Background(), u.ID); got.MFAExempt {
		t.Fatal("promotion to admin kept the exemption")
	}
	expect(t, admin.do("POST", "/api/admin/invites", map[string]any{"role": "admin", "mfa_exempt": true}), 400)

	// An invite can carry the exemption to the account it creates.
	inv := decode[map[string]any](t, admin.do("POST", "/api/admin/invites",
		map[string]any{"role": "user", "grant_path": "shared", "grant_perm": "write", "mfa_exempt": true}))
	token := inv["url"].(string)[strings.LastIndex(inv["url"].(string), "/")+1:]
	n := h.app()
	expect(t, n.do("POST", "/api/auth/invite/accept", map[string]string{"token": token, "username": "newbie", "password": testPassword}), 200)
	if restricted(n) != false {
		t.Fatal("exempt invite still requires a second factor")
	}
}

// A signed link is for the file it was made for, not a later one at its path.
func TestSignedLinkBoundToFileIdentity(t *testing.T) {
	h := newHarness(t)
	h.user("ivy", "admin")
	write(t, h.root, "doc.pdf", "%PDF-1.4 original")
	a := h.login("ivy")
	link := decode[map[string]string](t, a.do("POST", "/api/files/preview-url", map[string]string{"path": "doc.pdf"}))["url"]
	path := strings.TrimPrefix(link, shareOrigin)
	expect(t, h.share().do("GET", path, nil), 200)
	// Replaced as an upload does it: a new file renamed over the path.
	write(t, h.root, "new.pdf", "%PDF-1.4 replacement")
	if err := os.Rename(filepath.Join(h.root, "new.pdf"), filepath.Join(h.root, "doc.pdf")); err != nil {
		t.Fatal(err)
	}
	rec := h.share().do("GET", path, nil)
	if rec.Code != 404 || strings.Contains(rec.Body.String(), "replacement") {
		t.Fatalf("signed link served the replacement file: %d %q", rec.Code, rec.Body.String())
	}
}

// Linking a single sign-on identity adds a sign-in method: it needs a fresh
// login, and the session that asked for it must still stand at the callback.
func TestOIDCLinkNeedsFreshLiveSession(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	u := h.user("jan", "user", store.Grant{Path: "jan", Permission: "read"})
	link := map[string]string{"provider": "x"}
	rec := h.session(u, time.Hour).do("POST", "/api/me/oidc/link", link)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "reauth_required") {
		t.Fatalf("old session could start a link: %d %s", rec.Code, rec.Body.String())
	}
	// A fresh session gets as far as the (unconfigured) provider.
	expect(t, h.session(u, time.Minute).do("POST", "/api/me/oidc/link", link), 400)
	// Starting a link is not something a followed link can do.
	if rec := h.session(u, time.Minute).do("GET", "/api/auth/oidc/start?provider=x&link=1", nil); rec.Code != 400 || rec.Header().Get("Set-Cookie") != "" {
		t.Fatalf("GET start with link=1: %d %q", rec.Code, rec.Header().Get("Set-Cookie"))
	}

	hash := h.srv.keys.HashToken("link-session")
	now := time.Now()
	if err := h.store.CreateSession(ctx, &store.Session{IDHash: hash, UserID: u.ID, CreatedAt: now, LastSeenAt: now,
		ExpiresAt: now.Add(time.Hour), AuthMethod: "password"}); err != nil {
		t.Fatal(err)
	}
	ident := &auth.OIDCIdentity{LinkUserID: u.ID, LinkSession: hash}
	if !h.srv.linkSessionValid(ctx, ident) {
		t.Fatal("live session refused")
	}
	if h.srv.linkSessionValid(ctx, &auth.OIDCIdentity{LinkUserID: u.ID}) {
		t.Fatal("link without a session accepted")
	}
	if h.srv.linkSessionValid(ctx, &auth.OIDCIdentity{LinkUserID: u.ID + 1, LinkSession: hash}) {
		t.Fatal("link for another user accepted")
	}
	if err := h.store.DeleteSession(ctx, hash); err != nil {
		t.Fatal(err)
	}
	if h.srv.linkSessionValid(ctx, ident) {
		t.Fatal("link completed after the session was revoked")
	}
}

// What renders inline is decided by content: a page named like an image is
// served as a download, and the info endpoint reports what the file is.
func TestTypeComesFromContentNotName(t *testing.T) {
	h := newHarness(t)
	h.user("kai", "admin")
	write(t, h.root, "evil.jpg", "<script>alert(1)</script>")
	write(t, h.root, "real.png", "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	write(t, h.root, "noext", "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	c := h.login("kai")
	rec := c.do("GET", "/api/files/download?inline=1&path=evil.jpg", nil)
	if ct := rec.Header().Get("Content-Type"); ct != "application/octet-stream" ||
		!strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("script named .jpg served inline: %s %s", ct, rec.Header().Get("Content-Disposition"))
	}
	rec = c.do("GET", "/api/files/download?inline=1&path=noext", nil)
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("extensionless png served as %s", ct)
	}
	for range 2 { // the second answer comes from the cache
		info := decode[map[string]any](t, c.do("GET", "/api/files/info?path=evil.jpg", nil))
		if info["mime"] != "text/html" || info["mismatch"] != true || info["ext_mime"] != "image/jpeg" {
			t.Fatalf("info: %v", info)
		}
	}
	// Replaced content is sniffed again.
	write(t, h.root, "evil.jpg", "\xff\xd8\xff\xe0 now a real jpeg header")
	if info := decode[map[string]any](t, c.do("GET", "/api/files/info?path=evil.jpg", nil)); info["mime"] != "image/jpeg" || info["mismatch"] == true {
		t.Fatalf("info after replacing: %v", info)
	}
	list := decode[map[string]any](t, c.do("GET", "/api/files?path=", nil))
	for _, e := range list["entries"].([]any) {
		if e := e.(map[string]any); e["name"] == "noext" && e["mime"] != "image/png" {
			t.Fatalf("extensionless file listed as %v", e["mime"])
		}
	}
	expect(t, c.do("GET", "/api/files/archive?path=real.png", nil), 422)
}

// An upload in progress holds its quota for as long as it exists: across a
// restart and however old it is, until it is finished or cleaned up.
func TestUploadReservationOutlivesRestart(t *testing.T) {
	h := newHarness(t)
	u := h.user("jon", "user", store.Grant{Path: "jon", Permission: "write"})
	q := int64(100)
	if err := h.store.UpdateUser(context.Background(), u.ID, store.UserUpdate{QuotaBytes: &q}); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(h.root, "jon"), 0o755)
	c := h.login("jon")
	start := func(n int) int {
		c.headers["Tus-Resumable"] = "1.0.0"
		c.headers["Upload-Length"] = strconv.Itoa(n)
		c.headers["Upload-Metadata"] = tusMeta("filename", "big.bin", "dir", "jon")
		defer delete(c.headers, "Upload-Length")
		defer delete(c.headers, "Upload-Metadata")
		return c.do("POST", "/api/uploads/", []byte{}).Code
	}
	if code := start(80); code != 201 {
		t.Fatalf("first upload: %d", code)
	}
	// A new process finds the upload on disk and reserves its bytes again.
	fsvc := files.NewService(h.srv.files.FS, h.store)
	if _, err := uploads.New(fsvc, h.srv.shares, h.srv.audit, appOrigin, shareOrigin, 1<<30, nil, nil); err != nil {
		t.Fatal(err)
	}
	a, err := files.AccessFor(context.Background(), h.store, u)
	if err != nil {
		t.Fatal(err)
	}
	if fsvc.Reserve(context.Background(), "second", u.ID, a, 80, q) {
		t.Fatal("the upload in progress was not reserved after a restart")
	}
	// Cleaning up uploads that are not stale releases nothing.
	h.srv.uploads.Cleanup(time.Hour)
	if code := start(80); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("second upload while the first is in progress: %d", code)
	}
	// Once the first is cleaned up as stale, its quota is free again.
	h.srv.uploads.Cleanup(-time.Second)
	if code := start(80); code != 201 {
		t.Fatalf("upload after cleanup: %d", code)
	}
}

// A signed link stops with its session's idle timeout, not only when the
// expired row is finally swept.
func TestSignedLinkHonoursIdleTimeout(t *testing.T) {
	h := newHarness(t)
	u := h.user("kim", "admin")
	write(t, h.root, "doc.pdf", "%PDF-1.4")
	a := h.session(u, 0)
	link := decode[map[string]string](t, a.do("POST", "/api/files/preview-url", map[string]string{"path": "doc.pdf"}))["url"]
	path := strings.TrimPrefix(link, shareOrigin)
	expect(t, h.share().do("GET", path, nil), 200)
	hash := h.srv.keys.HashToken(a.cookies["fb_session"].Value)
	idle := h.srv.cfg.Session.IdleTimeout.Duration
	if err := h.store.TouchSession(context.Background(), hash, time.Now().Add(-idle-time.Minute)); err != nil {
		t.Fatal(err)
	}
	expect(t, h.share().do("GET", path, nil), 404)
}

// The cookie that lets a browser go on using a download it was counted for
// runs out, so a copy of it is not a pass for as long as the link lasts.
func TestShareCountedCookieExpires(t *testing.T) {
	h := newHarness(t)
	h.user("lea", "admin")
	write(t, h.root, "one.txt", "once")
	a := h.login("lea")
	created := decode[map[string]any](t, a.do("POST", "/api/shares", map[string]any{"path": "one.txt", "max_downloads": 1}))
	token := strings.TrimPrefix(created["url"].(string), shareOrigin+"/s/")
	first := h.share()
	expect(t, first.do("GET", "/s/"+token+"/file", nil), 200)
	expect(t, first.do("GET", "/s/"+token+"/file", nil), 200) // the same browser, again
	expect(t, h.share().do("GET", "/s/"+token+"/file", nil), 410)
	var counted *http.Cookie
	for _, c := range first.cookies {
		counted = c
	}
	if counted == nil || time.Until(counted.Expires) > 25*time.Hour {
		t.Fatalf("counted cookie = %+v", counted)
	}
	// A cookie in the old form (no expiry) no longer counts.
	old := h.share()
	sh, _ := h.store.Shares(context.Background(), 0)
	sig := h.srv.keys.Sign("share-counted", []byte(strconv.FormatInt(sh[0].ID, 10)))
	old.cookies[counted.Name] = &http.Cookie{Name: counted.Name, Value: base64.RawURLEncoding.EncodeToString(sig)}
	expect(t, old.do("GET", "/s/"+token+"/file", nil), 410)
}

// Removing the last second factor is decided under one lock with the other
// way of removing one, and a session stops vouching for a factor that is gone.
func TestLastFactorRemoval(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	u := h.user("mia", "user", store.Grant{Path: "mia", Permission: "write"})
	if err := h.store.EnableTOTP(ctx, u.ID, []byte("enc"), 0); err != nil {
		t.Fatal(err)
	}
	c := h.session(u, 0) // a session that presented a second factor
	if me := decode[map[string]any](t, c.do("GET", "/api/me", nil)); me["restricted"] != false {
		t.Fatalf("me = %v", me)
	}
	// TOTP is the only factor of an account that needs one: it stays.
	if err := h.store.DisableTOTP(ctx, u.ID, true); !errors.Is(err, store.ErrLastLogin) {
		t.Fatalf("DisableTOTP(keepOne) = %v", err)
	}
	// Where none is required (or an admin resets them), the session is no
	// longer one that presented a factor.
	if err := h.store.DisableTOTP(ctx, u.ID, false); err != nil {
		t.Fatal(err)
	}
	if me := decode[map[string]any](t, c.do("GET", "/api/me", nil)); me["restricted"] != true {
		t.Fatalf("after the last factor went: %v", me)
	}
}

// After a login provider, the pending login awaiting a second factor is in
// a cookie, not in the address.
func TestPendingLoginFromCookie(t *testing.T) {
	h := newHarness(t)
	u := h.user("nia", "user", store.Grant{Path: "nia", Permission: "read"})
	if err := h.store.EnableTOTP(context.Background(), u.ID, []byte("enc"), 0); err != nil {
		t.Fatal(err)
	}
	c := h.app()
	body := map[string]string{"token": "", "code": "000000"}
	if rec := c.do("POST", "/api/auth/login/totp", body); !strings.Contains(rec.Body.String(), "login_expired") {
		t.Fatalf("without a pending login: %d %s", rec.Code, rec.Body.String())
	}
	c.cookies["fb_pending"] = &http.Cookie{Name: "fb_pending", Value: h.srv.pending.add(u.ID, "oidc")}
	rec := c.do("POST", "/api/auth/login/totp", body)
	if rec.Code != http.StatusUnauthorized || strings.Contains(rec.Body.String(), "login_expired") {
		t.Fatalf("pending login from the cookie: %d %s", rec.Code, rec.Body.String())
	}
}

// An API token acts within the part of its user's access it was made for,
// on the routes that take tokens, and nowhere else.
func TestAPITokens(t *testing.T) {
	h := newHarness(t)
	u := h.user("omar", "user", store.Grant{Path: "omar", Permission: "write"}, store.Grant{Path: "lib", Permission: "read"})
	write(t, h.root, "omar/in/a.txt", "a")
	write(t, h.root, "omar/private/p.txt", "p")
	write(t, h.root, "lib/book.txt", "b")
	write(t, h.root, "other/x.txt", "x")

	// Made from a fresh session only, within what the user has.
	mk := map[string]any{"name": "scanner", "paths": []string{"omar/in"}, "caps": []string{"upload"}}
	expect(t, h.session(u, time.Hour).do("POST", "/api/me/tokens", mk), 401)
	c := h.session(u, time.Minute)
	for _, bad := range []map[string]any{
		{"name": "x", "paths": []string{"other"}, "caps": []string{"read"}},
		{"name": "x", "paths": []string{"lib"}, "caps": []string{"upload"}}, // read-only grant
		{"name": "x", "paths": []string{"omar"}, "caps": []string{"admin"}},
		{"name": "x", "paths": []string{"omar"}, "caps": []string{}},
		{"name": "", "paths": []string{"omar"}, "caps": []string{"read"}},
		{"name": "x", "paths": []string{"omar"}, "caps": []string{"read"}, "allowed_ips": []string{"nonsense"}},
	} {
		if rec := c.do("POST", "/api/me/tokens", bad); rec.Code < 400 {
			t.Errorf("token %v: %d", bad, rec.Code)
		}
	}
	created := decode[map[string]any](t, c.do("POST", "/api/me/tokens", mk))
	upload, _ := created["token"].(string)
	if !strings.HasPrefix(upload, "fbt_") {
		t.Fatalf("created = %v", created)
	}
	mk = map[string]any{"name": "reader", "paths": []string{""}, "caps": []string{"read"}, "allowed_ips": []string{"192.0.2.0/24"}}
	reader, _ := decode[map[string]any](t, c.do("POST", "/api/me/tokens", mk))["token"].(string)

	api := func(tok string) *client {
		a := h.app()
		a.origin = "" // a script: no browser, no Origin
		a.headers["Authorization"] = "Bearer " + tok
		return a
	}
	// The upload token uploads into its folder, and does nothing else.
	up := api(upload)
	rec, _ := up.tusUpload("/api/uploads/", "omar/in", "scan.txt", "scanned")
	expect(t, rec, 204)
	rec, _ = up.tusUpload("/api/uploads/", "omar/private", "scan.txt", "x")
	expect(t, rec, 403)
	expect(t, up.do("POST", "/api/files/mkdir", map[string]string{"path": "omar/in", "name": "2026"}), 200)
	expect(t, up.do("GET", "/api/files?path=omar/in", nil), 403)                                           // no read
	expect(t, up.do("POST", "/api/files/delete", map[string]any{"paths": []string{"omar/in/a.txt"}}), 403) // no modify
	// No token reaches the account, the admin pages or a session.
	for _, p := range []string{"/api/me", "/api/me/tokens", "/api/me/sessions", "/api/admin/users", "/api/trash"} {
		if rec := up.do("GET", p, nil); rec.Code != 403 || !strings.Contains(rec.Body.String(), "token_not_accepted") {
			t.Errorf("GET %s with a token: %d %s", p, rec.Code, rec.Body.String())
		}
	}
	expect(t, up.do("POST", "/api/me/password", map[string]string{"current": "x", "new": "y"}), 403)

	// The read token reads all its user can, from its addresses, read-only.
	rd := api(reader)
	expect(t, rd.do("GET", "/api/files/download?path=omar/private/p.txt", nil), 200)
	expect(t, rd.do("GET", "/api/files/download?path=lib/book.txt", nil), 200)
	expect(t, rd.do("GET", "/api/files/download?path=other/x.txt", nil), 404) // as for its user: not there
	rec, _ = rd.tusUpload("/api/uploads/", "omar/in", "nope.txt", "x")
	expect(t, rec, 403)
	rd.ip = "198.51.100.7"
	expect(t, rd.do("GET", "/api/files?path=omar", nil), 403)
	rd.ip = ""
	// It follows its user's grants as they change.
	if err := h.store.SetGrants(context.Background(), u.ID, []store.Grant{{Path: "omar", Permission: "write"}}); err != nil {
		t.Fatal(err)
	}
	expect(t, rd.do("GET", "/api/files/download?path=lib/book.txt", nil), 404)

	// A cookie beside a token is not looked at; a bad token is refused.
	both := h.session(u, time.Minute)
	both.headers["Authorization"] = "Bearer " + upload
	expect(t, both.do("GET", "/api/me", nil), 403)
	expect(t, api("fbt_"+strings.Repeat("A", 43)).do("GET", "/api/files?path=omar", nil), 401)

	// Revoked, it is gone.
	list := decode[[]map[string]any](t, c.do("GET", "/api/me/tokens", nil))
	if len(list) != 2 || list[0]["hint"] == "" || list[0]["token_hash"] != nil {
		t.Fatalf("tokens = %v", list)
	}
	for _, tk := range list {
		expect(t, c.do("DELETE", "/api/me/tokens/"+itoa(int64(tk["id"].(float64))), nil), 200)
	}
	expect(t, rd.do("GET", "/api/files?path=omar", nil), 401)
	rec, _ = up.tusUpload("/api/uploads/", "omar/in", "late.txt", "x")
	expect(t, rec, 401)
}

func TestAccessNarrow(t *testing.T) {
	owner := &files.Access{Grants: []store.Grant{{Path: "a", Permission: "write"}, {Path: "b/c", Permission: "read"}}}
	for _, tc := range []struct {
		paths []string
		write bool
		p     string
		want  files.Perm
	}{
		{[]string{""}, true, "a/x", files.PermWrite},
		{[]string{""}, true, "b/c/x", files.PermRead}, // never more than the owner
		{[]string{""}, false, "a/x", files.PermRead},
		{[]string{"a/sub"}, true, "a/sub/x", files.PermWrite},
		{[]string{"a/sub"}, true, "a/other", files.PermNone},
		{[]string{"b"}, true, "b/c/x", files.PermRead},
		{[]string{"b"}, true, "b/d", files.PermNone},
		{[]string{"z"}, true, "z", files.PermNone},
	} {
		if got := owner.Narrow(tc.paths, tc.write).Perm(tc.p); got != tc.want {
			t.Errorf("Narrow(%v, %v).Perm(%q) = %v, want %v", tc.paths, tc.write, tc.p, got, tc.want)
		}
	}
	admin := &files.Access{Admin: true}
	if n := admin.Narrow([]string{"x"}, true); n.Admin || n.Perm("x/y") != files.PermWrite || n.Perm("y") != files.PermNone {
		t.Errorf("admin narrowed = %+v", n)
	}
}

// Files can be uploaded in one request, by PUT or as a multipart form.
func TestOneRequestUploads(t *testing.T) {
	h := newHarness(t)
	u := h.user("pia", "user", store.Grant{Path: "pia", Permission: "write"}, store.Grant{Path: "ro", Permission: "read"})
	os.MkdirAll(filepath.Join(h.root, "pia"), 0o755)
	os.MkdirAll(filepath.Join(h.root, "ro"), 0o755)
	c := h.session(u, time.Minute)
	token := func(caps ...string) *client {
		rec := c.do("POST", "/api/me/tokens", map[string]any{"name": "t", "paths": []string{"pia"}, "caps": caps})
		expect(t, rec, 200)
		a := h.app()
		a.origin = ""
		a.headers["Authorization"] = "Bearer " + decode[map[string]any](t, rec)["token"].(string)
		return a
	}
	up := token("upload")

	rec := up.do("PUT", "/api/v1/files/pia/new/dir/report.txt", []byte("report"))
	expect(t, rec, 201)
	if got := decode[map[string]any](t, rec); got["path"] != "pia/new/dir/report.txt" || got["size"] != float64(6) {
		t.Fatalf("PUT answered %v", got)
	}
	// Never overwritten: the second gets the next free name.
	rec = up.do("PUT", "/api/v1/files/pia/new/dir/report.txt", []byte("again"))
	if got := decode[map[string]any](t, rec); got["name"] != "report (1).txt" {
		t.Fatalf("second PUT answered %v", got)
	}
	if got, _ := os.ReadFile(filepath.Join(h.root, "pia/new/dir/report.txt")); string(got) != "report" {
		t.Fatalf("first file is now %q", got)
	}
	for _, p := range []string{"ro/x.txt", "elsewhere/x.txt", "pia/..%2f..%2fx.txt", "pia/a:b.txt", "pia/.filebrowser/x", ""} {
		if rec := up.do("PUT", "/api/v1/files/"+p, []byte("x")); rec.Code < 400 {
			t.Errorf("PUT %q: %d", p, rec.Code)
		}
	}
	// Nothing is left in staging.
	left, _ := filepath.Glob(filepath.Join(h.root, ".filebrowser", "uploads", "put-*"))
	if len(left) != 0 {
		t.Errorf("staged files left: %v", left)
	}

	// The quota holds.
	q := int64(40)
	if err := h.store.UpdateUser(context.Background(), u.ID, store.UserUpdate{QuotaBytes: &q}); err != nil {
		t.Fatal(err)
	}
	expect(t, up.do("PUT", "/api/v1/files/pia/big.bin", []byte(strings.Repeat("x", 60))), http.StatusRequestEntityTooLarge)
	q = 0
	h.store.UpdateUser(context.Background(), u.ID, store.UserUpdate{QuotaBytes: &q})

	// A multipart form, as ShareX and HTML forms send.
	form := func(a *client, target string, fields ...string) *httptest.ResponseRecorder {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		for i := 0; i < len(fields); i += 2 {
			if name, ok := strings.CutPrefix(fields[i], "file:"); ok {
				fw, _ := mw.CreateFormFile("file", name)
				fw.Write([]byte(fields[i+1]))
			} else {
				mw.WriteField(fields[i], fields[i+1])
			}
		}
		mw.Close()
		a.headers["Content-Type"] = mw.FormDataContentType()
		defer delete(a.headers, "Content-Type")
		return a.do("POST", target, body.Bytes())
	}
	rec = form(up, "/api/v1/upload?dir=pia/shots", "file:one.png", "1", "file:two.png", "22")
	expect(t, rec, 201)
	if got := decode[map[string][]map[string]any](t, rec)["files"]; len(got) != 2 || got[1]["path"] != "pia/shots/two.png" {
		t.Fatalf("form answered %v", got)
	}
	expect(t, form(up, "/api/v1/upload", "dir", "pia/field", "file:f.txt", "f"), 201)
	if _, err := os.Stat(filepath.Join(h.root, "pia/field/f.txt")); err != nil {
		t.Fatal(err)
	}
	expect(t, form(up, "/api/v1/upload?dir=ro", "file:x.txt", "x"), 404) // outside the token
	expect(t, form(up, "/api/v1/upload?dir=pia", "note", "no file"), 400)
	// A link to the upload needs the right to share.
	expect(t, form(up, "/api/v1/upload?dir=pia&share=1", "file:s.png", "s"), 403)
	rec = form(token("upload", "share"), "/api/v1/upload?dir=pia&share=1", "file:s.png", "shot")
	expect(t, rec, 201)
	link, _ := decode[map[string]any](t, rec)["url"].(string)
	if rec := h.share().do("GET", strings.TrimPrefix(link, shareOrigin)+"/file", nil); rec.Code != 200 || rec.Body.String() != "shot" {
		t.Fatalf("shared upload: %d %q (%s)", rec.Code, rec.Body.String(), link)
	}
}
