package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"filebrowser/internal/auth"
	"filebrowser/internal/config"
	"filebrowser/internal/files"
	"filebrowser/internal/media"
	"filebrowser/internal/store"
	"filebrowser/internal/store/storetest"
)

const (
	appOrigin   = "http://files.test"
	shareOrigin = "http://share.test"
)

type harness struct {
	t     *testing.T
	srv   *Server
	h     http.Handler
	store *store.Store
	root  string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st := storetest.New(t)
	root := t.TempDir()
	cfg := config.Default()
	cfg.AppOrigin, cfg.ShareOrigin, cfg.Dev = appOrigin, shareOrigin, true
	cfg.FilesRoot, cfg.DataDir, cfg.CacheDir = root, t.TempDir(), t.TempDir()
	keys, err := auth.NewKeys(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	fsys, err := files.OpenFS(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fsys.Close() })
	web := fstest.MapFS{
		"index.html":                  {Data: []byte(`<!doctype html><script>boot()</script><div id=app></div>`)},
		"_app/immutable/entry/app.js": {Data: []byte(`console.log(1)`)},
	}
	mcfg := cfg.Media
	mcfg.Sandbox = "none"
	msvc, err := media.NewService(mcfg, st, fsys, root, cfg.CacheDir)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(Deps{Config: cfg, Store: st, Keys: keys, Files: files.NewService(fsys, st), Media: msvc, Web: web})
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, srv: srv, h: srv.Handler(), store: st, root: root}
}

type client struct {
	ip      string // RemoteAddr host, default 192.0.2.1
	h       *harness
	host    string
	origin  string
	cookies map[string]*http.Cookie
	headers map[string]string
}

func (h *harness) app() *client {
	return &client{h: h, host: "files.test", origin: appOrigin, cookies: map[string]*http.Cookie{}, headers: map[string]string{}}
}

func (h *harness) share() *client {
	return &client{h: h, host: "share.test", origin: shareOrigin, cookies: map[string]*http.Cookie{}, headers: map[string]string{}}
}

func (c *client) do(method, target string, body any) *httptest.ResponseRecorder {
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	case string:
		rd = strings.NewReader(b)
	default:
		raw, _ := json.Marshal(b)
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, target, rd)
	req.Host = c.host
	if c.ip != "" {
		req.RemoteAddr = c.ip + ":1234"
	}
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("Origin", c.origin)
	}
	if _, ok := body.([]byte); !ok && body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	for _, ck := range c.cookies {
		req.AddCookie(ck)
	}
	rec := httptest.NewRecorder()
	c.h.h.ServeHTTP(rec, req)
	for _, ck := range rec.Result().Cookies() {
		if ck.MaxAge < 0 {
			delete(c.cookies, ck.Name)
		} else {
			c.cookies[ck.Name] = ck
		}
	}
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

func expect(t *testing.T, rec *httptest.ResponseRecorder, status int) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, status, rec.Body.String())
	}
}

const testPassword = "correct horse battery"

func (h *harness) user(name, role string, grants ...store.Grant) *store.User {
	h.t.Helper()
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		h.t.Fatal(err)
	}
	u, err := h.store.CreateUser(context.Background(), name, hash, role, 0)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.store.SetGrants(context.Background(), u.ID, grants); err != nil {
		h.t.Fatal(err)
	}
	return u
}

// login logs in and, if required, enrolls TOTP so the session is unrestricted.
func (h *harness) login(name string) *client {
	h.t.Helper()
	c := h.app()
	rec := c.do("POST", "/api/auth/login", map[string]string{"username": name, "password": testPassword})
	expect(h.t, rec, 200)
	me := decode[map[string]any](h.t, c.do("GET", "/api/me", nil))
	if me["restricted"] == true {
		setup := decode[map[string]string](h.t, c.do("POST", "/api/auth/totp/setup", struct{}{}))
		secret, err := base32Decode(setup["secret"])
		if err != nil {
			h.t.Fatal(err)
		}
		expect(h.t, c.do("POST", "/api/auth/totp/enable", map[string]string{"code": auth.TOTPCode(secret, time.Now())}), 200)
	}
	return c
}

func base32Decode(s string) ([]byte, error) {
	return base32NoPad.DecodeString(s)
}

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

var pathParam = regexp.MustCompile(`\{[a-z]+\}`)

func TestEveryProtectedRouteRequiresSession(t *testing.T) {
	h := newHarness(t)
	c := h.app()
	n := 0
	for _, rt := range h.srv.Routes {
		if rt.Level == Public {
			continue
		}
		method, path, ok := strings.Cut(rt.Pattern, " ")
		if !ok {
			method, path = "GET", rt.Pattern
		}
		path = pathParam.ReplaceAllString(path, "1")
		rec := c.do(method, path, struct{}{})
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without session: status %d", method, path, rec.Code)
		}
		n++
	}
	if n < 40 {
		t.Fatalf("only %d protected routes checked", n)
	}
	// Unknown API paths must not reveal anything either.
	expect(t, c.do("GET", "/api/does-not-exist", nil), 401)
	// A forged cookie is rejected.
	c.cookies["fb_session"] = &http.Cookie{Name: "fb_session", Value: auth.NewToken(32)}
	expect(t, c.do("GET", "/api/me", nil), 401)
}

func TestLoginTOTPAndRestrictedSession(t *testing.T) {
	h := newHarness(t)
	h.user("alice", "user", store.Grant{Path: "alice", Permission: "write"})
	os.MkdirAll(filepath.Join(h.root, "alice"), 0o755)
	c := h.app()

	expect(t, c.do("POST", "/api/auth/login", map[string]string{"username": "alice", "password": "wrong password!"}), 401)
	unknown := c.do("POST", "/api/auth/login", map[string]string{"username": "nobody", "password": "wrong password!"})
	expect(t, unknown, 401)
	if unknown.Body.String() != c.do("POST", "/api/auth/login", map[string]string{"username": "alice", "password": "wrong password!"}).Body.String() {
		t.Error("unknown and wrong-password responses differ")
	}

	expect(t, c.do("POST", "/api/auth/login", map[string]string{"username": "alice", "password": testPassword}), 200)
	// A writer without TOTP gets a restricted session.
	rec := c.do("GET", "/api/files?path=alice", nil)
	expect(t, rec, 403)
	if decode[map[string]string](t, rec)["code"] != "totp_enrollment_required" {
		t.Fatalf("unexpected body %s", rec.Body.String())
	}
	setup := decode[map[string]string](t, c.do("POST", "/api/auth/totp/setup", struct{}{}))
	secret, _ := base32Decode(setup["secret"])
	expect(t, c.do("POST", "/api/auth/totp/enable", map[string]string{"code": "000000"}), 400)
	oldCookie := c.cookies["fb_session"].Value
	expect(t, c.do("POST", "/api/auth/totp/enable", map[string]string{"code": auth.TOTPCode(secret, time.Now())}), 200)
	if c.cookies["fb_session"].Value == oldCookie {
		t.Error("session not rotated after enabling TOTP")
	}
	expect(t, c.do("GET", "/api/files?path=alice", nil), 200)

	// Fresh login now requires the second factor; the enrollment code cannot be replayed.
	c2 := h.app()
	resp := decode[map[string]any](t, c2.do("POST", "/api/auth/login", map[string]string{"username": "alice", "password": testPassword}))
	if resp["totp_required"] != true {
		t.Fatalf("expected totp step, got %v", resp)
	}
	tok := resp["token"].(string)
	expect(t, c2.do("POST", "/api/auth/login/totp", map[string]string{"token": tok, "code": auth.TOTPCode(secret, time.Now())}), 401)
	expect(t, c2.do("POST", "/api/auth/login/totp", map[string]string{"token": tok, "code": auth.TOTPCode(secret, time.Now().Add(30*time.Second))}), 200)
	expect(t, c2.do("GET", "/api/files?path=alice", nil), 200)

	// Logout invalidates the session server-side.
	cookie := *c2.cookies["fb_session"]
	expect(t, c2.do("POST", "/api/auth/logout", struct{}{}), 200)
	c3 := h.app()
	c3.cookies["fb_session"] = &cookie
	expect(t, c3.do("GET", "/api/me", nil), 401)
}

func TestLoginBackoff(t *testing.T) {
	h := newHarness(t)
	h.user("bob", "user")
	c := h.app()
	var last int
	for i := 0; i < 8; i++ {
		last = c.do("POST", "/api/auth/login", map[string]string{"username": "bob", "password": "nope nope nope"}).Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after repeated failures, got %d", last)
	}
}

func TestCSRFAndHosts(t *testing.T) {
	h := newHarness(t)
	h.user("admin", "admin")
	c := h.login("admin")
	c.origin = "http://evil.test"
	expect(t, c.do("POST", "/api/files/mkdir", map[string]string{"path": "", "name": "x"}), 403)
	c.origin = ""
	req := httptest.NewRequest("POST", "/api/files/mkdir", strings.NewReader(`{"path":"","name":"x"}`))
	req.Host = "files.test"
	for _, ck := range c.cookies {
		req.AddCookie(ck)
	}
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, req)
	expect(t, rec, 403)

	// Unknown host.
	req = httptest.NewRequest("GET", "/", nil)
	req.Host = "other.test"
	rec = httptest.NewRecorder()
	h.h.ServeHTTP(rec, req)
	expect(t, rec, http.StatusMisdirectedRequest)

	// The API is not served on the share origin.
	expect(t, h.share().do("GET", "/api/me", nil), 404)
}

func TestSecurityHeadersAndSPA(t *testing.T) {
	h := newHarness(t)
	rec := h.app().do("GET", "/files/some/dir", nil)
	expect(t, rec, 200)
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "'sha256-") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("SPA CSP = %q", csp)
	}
	for _, hd := range []string{"X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy"} {
		if rec.Header().Get(hd) == "" {
			t.Errorf("missing %s", hd)
		}
	}
	rec = h.app().do("GET", "/_app/immutable/entry/app.js", nil)
	expect(t, rec, 200)
	if !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Error("immutable assets not cached")
	}
	expect(t, h.app().do("GET", "/_app/missing.js", nil), 404)
	// Share origin serves the SPA only under /s/.
	expect(t, h.share().do("GET", "/s/sometoken", nil), 200)
	expect(t, h.share().do("GET", "/files", nil), 404)
}

func TestTraversalAttempts(t *testing.T) {
	h := newHarness(t)
	h.user("carol", "user", store.Grant{Path: "carol", Permission: "read"})
	write(t, h.root, "carol/ok.txt", "ok")
	write(t, h.root, "secret/s.txt", "secret")
	write(t, h.root, ".filebrowser/trash/x/y.txt", "trash")
	c := h.login("carol")

	expect(t, c.do("GET", "/api/files/download?path=carol/ok.txt", nil), 200)
	attempts := []string{
		"../secret/s.txt", "carol/../secret/s.txt", "carol/../../etc/passwd", "%2e%2e/secret/s.txt",
		"carol/%2e%2e/secret/s.txt", "/secret/s.txt", "secret/s.txt", ".filebrowser/trash/x/y.txt",
		"carol\\..\\secret\\s.txt", "carol/%00", "C:/Windows/win.ini", "//etc/passwd",
	}
	for _, a := range attempts {
		for _, ep := range []string{"/api/files/download?path=", "/api/files?path=", "/api/thumb?path="} {
			rec := c.do("GET", ep+a, nil)
			if rec.Code == 200 {
				t.Errorf("%s%s returned 200: %s", ep, a, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "secret") && !strings.Contains(rec.Body.String(), "secret/") {
				t.Errorf("%s%s leaked content", ep, a)
			}
		}
	}
	// Writes outside the grant fail.
	for _, a := range []string{"carol", "../x", "secret"} {
		rec := c.do("POST", "/api/files/mkdir", map[string]string{"path": a, "name": "new"})
		if rec.Code == 200 {
			t.Errorf("mkdir in %q succeeded", a)
		}
	}
	rec := c.do("POST", "/api/files/mkdir", map[string]string{"path": "carol", "name": ".."})
	if rec.Code == 200 {
		t.Error("mkdir .. succeeded")
	}
}

func TestSymlinkEscapeHTTP(t *testing.T) {
	h := newHarness(t)
	h.user("admin", "admin")
	outside := t.TempDir()
	write(t, outside, "secret.txt", "secret")
	if err := os.Symlink(outside, filepath.Join(h.root, "link")); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	c := h.login("admin")
	if rec := c.do("GET", "/api/files/download?path=link/secret.txt", nil); rec.Code == 200 {
		t.Fatal("followed escaping symlink")
	}
}

func TestFileOpsAndTrash(t *testing.T) {
	h := newHarness(t)
	h.user("admin", "admin")
	c := h.login("admin")
	write(t, h.root, "docs/a.txt", "hello")
	expect(t, c.do("POST", "/api/files/mkdir", map[string]string{"path": "docs", "name": "sub"}), 200)
	expect(t, c.do("POST", "/api/files/rename", map[string]string{"path": "docs/a.txt", "name": "b.txt"}), 200)
	expect(t, c.do("POST", "/api/files/move", map[string]any{"paths": []string{"docs/b.txt"}, "dest": "docs/sub"}), 200)
	rec := c.do("GET", "/api/files/download?path=docs/sub/b.txt", nil)
	expect(t, rec, 200)
	if rec.Body.String() != "hello" || !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("download: %q %q", rec.Body.String(), rec.Header().Get("Content-Disposition"))
	}
	// Range support.
	c.headers["Range"] = "bytes=1-2"
	rec = c.do("GET", "/api/files/download?path=docs/sub/b.txt", nil)
	delete(c.headers, "Range")
	if rec.Code != 206 || rec.Body.String() != "el" {
		t.Fatalf("range: %d %q", rec.Code, rec.Body.String())
	}
	// HTML is never inline.
	write(t, h.root, "docs/x.html", "<script>alert(1)</script>")
	rec = c.do("GET", "/api/files/download?path=docs/x.html&inline=1", nil)
	if !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment") || rec.Header().Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("html served inline: %v", rec.Header())
	}

	expect(t, c.do("POST", "/api/files/delete", map[string]any{"paths": []string{"docs/sub"}}), 200)
	if _, err := os.Stat(filepath.Join(h.root, "docs", "sub")); !os.IsNotExist(err) {
		t.Fatal("deleted dir still present")
	}
	trash := decode[map[string]json.RawMessage](t, c.do("GET", "/api/trash", nil))
	var items []store.TrashItem
	json.Unmarshal(trash["items"], &items)
	if len(items) != 1 || items[0].OriginalPath != "docs/sub" {
		t.Fatalf("trash = %+v", items)
	}
	expect(t, c.do("POST", "/api/trash/restore", map[string]any{"ids": []string{items[0].ID}}), 200)
	if _, err := os.Stat(filepath.Join(h.root, "docs", "sub", "b.txt")); err != nil {
		t.Fatal("restore failed:", err)
	}
	expect(t, c.do("POST", "/api/files/delete", map[string]any{"paths": []string{"docs/sub"}}), 200)
	expect(t, c.do("POST", "/api/trash/purge", map[string]any{"all": true}), 200)
	if ents, _ := os.ReadDir(filepath.Join(h.root, ".filebrowser", "trash")); len(ents) != 0 {
		t.Fatalf("trash dir not empty: %v", ents)
	}
	// Zip.
	write(t, h.root, "zipme/one.txt", "1")
	write(t, h.root, "zipme/d/two.txt", "2")
	rec = c.do("GET", "/api/files/zip?paths=zipme", nil)
	expect(t, rec, 200)
	if !bytes.HasPrefix(rec.Body.Bytes(), []byte("PK")) {
		t.Fatal("not a zip")
	}
	// Search.
	res := decode[map[string]json.RawMessage](t, c.do("GET", "/api/search?q=two", nil))
	if !strings.Contains(string(res["results"]), "zipme/d/two.txt") {
		t.Fatalf("search: %s", res["results"])
	}
}

func TestSharesEndToEnd(t *testing.T) {
	h := newHarness(t)
	h.user("dave", "user", store.Grant{Path: "dave", Permission: "write"})
	write(t, h.root, "dave/album/song.flac", "flacdata")
	write(t, h.root, "dave/album/cover.jpg", "jpg")
	write(t, h.root, "dave/private.txt", "private")
	c := h.login("dave")

	// Shares outside the grant are refused.
	rec := c.do("POST", "/api/shares", map[string]any{"path": "elsewhere"})
	if rec.Code == 200 {
		t.Fatal("shared path outside grant")
	}
	created := decode[map[string]any](t, c.do("POST", "/api/shares", map[string]any{"path": "dave/album", "password": "s3cret-pass", "max_downloads": 2}))
	link := created["url"].(string)
	token := strings.TrimPrefix(link, shareOrigin+"/s/")
	if token == link || len(token) < 22 {
		t.Fatalf("bad url %q", link)
	}
	s := h.share()
	info := decode[map[string]any](t, s.do("GET", "/s/"+token+"/api/info", nil))
	if info["needs_password"] != true || info["unlocked"] != false {
		t.Fatalf("info = %v", info)
	}
	expect(t, s.do("GET", "/s/"+token+"/api/list", nil), 401)
	expect(t, s.do("POST", "/s/"+token+"/api/unlock", map[string]string{"password": "wrong"}), 401)
	expect(t, s.do("POST", "/s/"+token+"/api/unlock", map[string]string{"password": "s3cret-pass"}), 200)
	list := decode[map[string]json.RawMessage](t, s.do("GET", "/s/"+token+"/api/list", nil))
	if !strings.Contains(string(list["entries"]), `"song.flac"`) || strings.Contains(string(list["entries"]), "dave/") {
		t.Fatalf("share listing leaks or misses: %s", list["entries"])
	}
	// Traversal out of the share.
	for _, p := range []string{"../private.txt", "%2e%2e/private.txt", "/../private.txt"} {
		if rec := s.do("GET", "/s/"+token+"/file?path="+p, nil); rec.Code == 200 {
			t.Errorf("share traversal %q succeeded", p)
		}
	}
	rec = s.do("GET", "/s/"+token+"/file?path=song.flac&inline=1", nil)
	expect(t, rec, 200)
	if rec.Body.String() != "flacdata" || !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "inline") {
		t.Fatalf("inline flac: %v", rec.Header())
	}
	expect(t, s.do("GET", "/s/"+token+"/file?path=cover.jpg", nil), 200) // same browser: counted once
	for i, want := range []int{200, http.StatusGone} {                   // limit 2: one more browser
		other := h.share()
		other.cookies = map[string]*http.Cookie{}
		for k, v := range s.cookies {
			if strings.HasPrefix(k, "fbs") {
				other.cookies[k] = v
			}
		}
		if rec := other.do("GET", "/s/"+token+"/file?path=cover.jpg", nil); rec.Code != want {
			t.Fatalf("browser %d: status %d, want %d", i+2, rec.Code, want)
		}
	}

	// Revocation.
	shares := decode[[]map[string]any](t, c.do("GET", "/api/shares", nil))
	id := int64(shares[0]["id"].(float64))
	expect(t, c.do("DELETE", "/api/shares/"+itoa(id), nil), 200)
	expect(t, s.do("GET", "/s/"+token+"/api/info", nil), 404)

	// Another user cannot revoke dave's share.
	created = decode[map[string]any](t, c.do("POST", "/api/shares", map[string]any{"path": "dave/private.txt"}))
	h.user("eve", "user", store.Grant{Path: "eve", Permission: "read"})
	e := h.login("eve")
	sid := int64(created["share"].(map[string]any)["id"].(float64))
	expect(t, e.do("DELETE", "/api/shares/"+itoa(sid), nil), 404)
	// Dropping the owner's grant disables the share.
	tok2 := strings.TrimPrefix(created["url"].(string), shareOrigin+"/s/")
	expect(t, s.do("GET", "/s/"+tok2+"/file", nil), 200)
	ctx := context.Background()
	u, _ := h.store.UserByName(ctx, "dave")
	h.store.SetGrants(ctx, u.ID, nil)
	expect(t, s.do("GET", "/s/"+tok2+"/file", nil), 404)
	// Bogus tokens.
	expect(t, s.do("GET", "/s/AAAAAAAAAAAAAAAAAAAAAAAA/api/info", nil), 404)
}

func itoa(n int64) string { return strconvFormat(n) }

func tusMeta(kv ...string) string {
	var parts []string
	for i := 0; i < len(kv); i += 2 {
		parts = append(parts, kv[i]+" "+base64.StdEncoding.EncodeToString([]byte(kv[i+1])))
	}
	return strings.Join(parts, ",")
}

func (c *client) tusUpload(prefix, dir, name, data string) (*httptest.ResponseRecorder, string) {
	c.headers["Tus-Resumable"] = "1.0.0"
	c.headers["Upload-Length"] = itoa(int64(len(data)))
	c.headers["Upload-Metadata"] = tusMeta("filename", name, "dir", dir)
	rec := c.do("POST", prefix, []byte{})
	delete(c.headers, "Upload-Length")
	delete(c.headers, "Upload-Metadata")
	if rec.Code != 201 {
		return rec, ""
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	c.headers["Upload-Offset"] = "0"
	c.headers["Content-Type"] = "application/offset+octet-stream"
	rec = c.do("PATCH", loc.Path, []byte(data))
	delete(c.headers, "Upload-Offset")
	delete(c.headers, "Content-Type")
	return rec, loc.Path
}

func TestTusUploads(t *testing.T) {
	h := newHarness(t)
	h.user("fran", "user", store.Grant{Path: "fran", Permission: "write"}, store.Grant{Path: "ro", Permission: "read"})
	os.MkdirAll(filepath.Join(h.root, "fran"), 0o755)
	os.MkdirAll(filepath.Join(h.root, "ro"), 0o755)
	c := h.login("fran")

	rec, _ := c.tusUpload("/api/uploads/", "fran", "hello.txt", "hello world")
	expect(t, rec, 204)
	if got, _ := os.ReadFile(filepath.Join(h.root, "fran", "hello.txt")); string(got) != "hello world" {
		t.Fatalf("uploaded content = %q", got)
	}
	// Same name gets a unique suffix.
	rec, _ = c.tusUpload("/api/uploads/", "fran", "hello.txt", "again")
	expect(t, rec, 204)
	if _, err := os.Stat(filepath.Join(h.root, "fran", "hello (1).txt")); err != nil {
		t.Fatal(err)
	}
	// Read-only and out-of-grant destinations are refused at creation.
	for _, dir := range []string{"ro", "", "../x", ".filebrowser"} {
		rec, _ = c.tusUpload("/api/uploads/", dir, "x.txt", "x")
		if rec.Code < 400 {
			t.Errorf("upload to %q: %d", dir, rec.Code)
		}
	}
	for _, name := range []string{"../x.txt", "a/b.txt", ".."} {
		rec, _ = c.tusUpload("/api/uploads/", "fran", name, "x")
		if rec.Code < 400 {
			t.Errorf("upload name %q: %d", name, rec.Code)
		}
	}
	// Another user cannot resume someone else's upload.
	c.headers["Tus-Resumable"] = "1.0.0"
	c.headers["Upload-Length"] = "10"
	c.headers["Upload-Metadata"] = tusMeta("filename", "big.bin", "dir", "fran")
	created := c.do("POST", "/api/uploads/", []byte{})
	expect(t, created, 201)
	loc, _ := url.Parse(created.Header().Get("Location"))
	h.user("other", "user", store.Grant{Path: "other", Permission: "write"})
	o := h.login("other")
	o.headers["Tus-Resumable"] = "1.0.0"
	o.headers["Upload-Offset"] = "0"
	o.headers["Content-Type"] = "application/offset+octet-stream"
	expect(t, o.do("PATCH", loc.Path, []byte("0123456789")), 404)
	expect(t, o.do("HEAD", loc.Path, nil), 404)
}

func TestDropShareUpload(t *testing.T) {
	h := newHarness(t)
	h.user("gina", "user", store.Grant{Path: "gina", Permission: "write"})
	os.MkdirAll(filepath.Join(h.root, "gina", "inbox"), 0o755)
	c := h.login("gina")
	created := decode[map[string]any](t, c.do("POST", "/api/shares", map[string]any{"path": "gina/inbox", "kind": "upload"}))
	token := strings.TrimPrefix(created["url"].(string), shareOrigin+"/s/")
	s := h.share()
	// Drop links do not allow listing or downloads.
	expect(t, s.do("GET", "/s/"+token+"/api/list", nil), 404)
	s.headers["X-Share-Token"] = token
	rec, _ := s.tusUpload("/_upload/", "", "dropped.txt", "dropped")
	expect(t, rec, 204)
	if got, _ := os.ReadFile(filepath.Join(h.root, "gina", "inbox", "dropped.txt")); string(got) != "dropped" {
		t.Fatalf("dropped content = %q", got)
	}
	rec, _ = s.tusUpload("/_upload/", "../..", "escape.txt", "x")
	if rec.Code < 400 {
		t.Fatalf("drop upload escaped: %d", rec.Code)
	}
	s.headers["X-Share-Token"] = "invalid-token-invalid-token"
	rec, _ = s.tusUpload("/_upload/", "", "x.txt", "x")
	expect(t, rec, 404)
}

func TestAdminAndInvites(t *testing.T) {
	h := newHarness(t)
	h.user("root", "admin")
	h.user("plain", "user", store.Grant{Path: "plain", Permission: "read"})
	a := h.login("root")
	p := h.login("plain")
	expect(t, p.do("GET", "/api/admin/users", nil), 403)
	users := decode[[]map[string]any](t, a.do("GET", "/api/admin/users", nil))
	if len(users) != 2 {
		t.Fatalf("users = %v", users)
	}
	// A blank grant path is rejected rather than meaning "everything".
	plainU, _ := h.store.UserByName(context.Background(), "plain")
	expect(t, a.do("PUT", "/api/admin/users/"+itoa(plainU.ID)+"/grants", []map[string]string{{"path": "", "permission": "read"}}), 400)
	expect(t, a.do("PUT", "/api/admin/users/"+itoa(plainU.ID)+"/grants", []map[string]string{{"path": "/", "permission": "read"}}), 200)
	expect(t, a.do("PUT", "/api/admin/users/"+itoa(plainU.ID)+"/grants", []map[string]string{{"path": "plain", "permission": "read"}}), 200)
	// Cannot demote the last admin (self).
	root, _ := h.store.UserByName(context.Background(), "root")
	rootID := root.ID
	expect(t, a.do("PATCH", "/api/admin/users/"+itoa(rootID), map[string]any{"role": "user"}), 400)

	inv := decode[map[string]any](t, a.do("POST", "/api/admin/invites", map[string]any{"role": "user", "grant_path": "shared", "grant_perm": "read"}))
	token := strings.TrimPrefix(inv["url"].(string), appOrigin+"/invite/")
	n := h.app()
	expect(t, n.do("GET", "/api/auth/invite?token="+token, nil), 200)
	expect(t, n.do("POST", "/api/auth/invite/accept", map[string]string{"token": token, "username": "newbie", "password": "short"}), 400)
	expect(t, n.do("POST", "/api/auth/invite/accept", map[string]string{"token": token, "username": "newbie", "password": testPassword}), 200)
	me := decode[map[string]any](t, n.do("GET", "/api/me", nil))
	if me["user"].(map[string]any)["username"] != "newbie" {
		t.Fatalf("me = %v", me)
	}
	// Invites are single-use.
	expect(t, h.app().do("POST", "/api/auth/invite/accept", map[string]string{"token": token, "username": "second", "password": testPassword}), 404)

	audit := decode[[]map[string]any](t, a.do("GET", "/api/admin/audit?action=auth.", nil))
	if len(audit) == 0 {
		t.Fatal("no audit entries")
	}
	// Disabling a user kills their sessions.
	plain, _ := h.store.UserByName(context.Background(), "plain")
	expect(t, a.do("PATCH", "/api/admin/users/"+itoa(plain.ID), map[string]any{"disabled": true}), 200)
	expect(t, p.do("GET", "/api/me", nil), 401)
}

func TestHealth(t *testing.T) {
	h := newHarness(t)
	rec := h.app().do("GET", "/api/health", nil)
	expect(t, rec, 200)
	if decode[map[string]string](t, rec)["status"] != "ok" {
		t.Fatalf("body %s", rec.Body.String())
	}
}
