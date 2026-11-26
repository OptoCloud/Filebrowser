package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filebrowser/internal/store"
	"filebrowser/internal/webhooks"
)

func TestWebhooks(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.srv.hooks.Run(ctx)

	type call struct {
		event, ts, sig string
		body           []byte
	}
	calls := make(chan call, 10)
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls <- call{r.Header.Get("X-Fb-Event"), r.Header.Get("X-Fb-Timestamp"), r.Header.Get("X-Fb-Signature"), body}
	}))
	defer recv.Close()
	wait := func() call {
		t.Helper()
		select {
		case c := <-calls:
			return c
		case <-time.After(5 * time.Second):
			t.Fatal("no webhook call")
			return call{}
		}
	}

	admin := h.user("root", "admin")
	plain := h.user("quin", "user", store.Grant{Path: "quin", Permission: "write"})
	os.MkdirAll(filepath.Join(h.root, "quin"), 0o755)
	os.MkdirAll(filepath.Join(h.root, "in", "docs"), 0o755)

	// Only an administrator's hooks may point into the server's own network,
	// and only a fresh session sets one up.
	p := h.session(plain, time.Minute)
	hook := func(url string) map[string]any {
		return map[string]any{"name": "n", "url": url, "events": []string{webhooks.FileUploaded}}
	}
	for _, u := range []string{recv.URL, "https://127.0.0.1/x", "https://10.1.2.3/", "https://[::1]/", "https://169.254.169.254/",
		"http://example.com/", "ftp://example.com/", "https://user:pw@example.com/", "nonsense"} {
		if rec := p.do("POST", "/api/me/webhooks", hook(u)); rec.Code != 400 {
			t.Errorf("user webhook to %s: %d", u, rec.Code)
		}
	}
	expect(t, p.do("POST", "/api/me/webhooks", hook("https://example.com/hook")), 200)
	expect(t, h.session(plain, time.Hour).do("POST", "/api/me/webhooks", hook("https://example.com/hook")), 401)
	expect(t, p.do("POST", "/api/me/webhooks", map[string]any{"name": "n", "url": "https://example.com/", "events": []string{"everything"}}), 400)

	a := h.session(admin, time.Minute)
	req := hook(recv.URL)
	req["path"] = "in"
	rec := a.do("POST", "/api/me/webhooks", req)
	expect(t, rec, 200)
	created := decode[map[string]any](t, rec)
	secret, _ := created["secret"].(string)
	id := itoa(int64(created["webhook"].(map[string]any)["id"].(float64)))
	if secret == "" || created["webhook"].(map[string]any)["secret"] != nil {
		t.Fatalf("created = %v", created)
	}

	// An upload below the hook's path is told, signed; one elsewhere is not.
	r, _ := a.tusUpload("/api/uploads/", "quin", "elsewhere.txt", "x")
	expect(t, r, 204)
	r, _ = a.tusUpload("/api/uploads/", "in/docs", "scan.pdf", "%PDF-1.4")
	expect(t, r, 204)
	c := wait()
	var msg struct {
		Event, Path string
		Data        map[string]any
	}
	if err := json.Unmarshal(c.body, &msg); err != nil {
		t.Fatal(err)
	}
	if c.event != webhooks.FileUploaded || msg.Event != c.event || msg.Path != "in/docs/scan.pdf" || msg.Data["size"] != float64(8) {
		t.Fatalf("call = %s %s", c.event, c.body)
	}
	if want := webhooks.Sign([]byte(secret), c.ts, c.body); c.sig != want || c.ts == "" {
		t.Fatalf("signature %q, want %q", c.sig, want)
	}

	expect(t, a.do("POST", "/api/me/webhooks/"+id+"/test", struct{}{}), 200)
	if c := wait(); c.event != webhooks.Ping {
		t.Fatalf("test sent %s", c.event)
	}
	// Another user cannot test or delete it.
	expect(t, p.do("POST", "/api/me/webhooks/"+id+"/test", struct{}{}), 404)
	expect(t, p.do("DELETE", "/api/me/webhooks/"+id, nil), 404)

	// The outcome is on the hook.
	deadline := time.Now().Add(3 * time.Second)
	for {
		list := decode[[]map[string]any](t, a.do("GET", "/api/me/webhooks", nil))
		if len(list) == 1 && list[0]["last_status"] == float64(200) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("webhooks = %v", list)
		}
		time.Sleep(20 * time.Millisecond)
	}
	expect(t, a.do("DELETE", "/api/me/webhooks/"+id, nil), 200)
	r, _ = a.tusUpload("/api/uploads/", "in/docs", "later.pdf", "%PDF-1.4")
	expect(t, r, 204)
	select {
	case c := <-calls:
		t.Fatalf("a deleted webhook was called: %s", c.body)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestWebhookAddresses(t *testing.T) {
	for addr, public := range map[string]bool{
		"93.184.216.34:443": true, "[2606:2800:220:1:248:1893:25c8:1946]:443": true,
		"127.0.0.1:80": false, "10.0.0.1:443": false, "192.168.1.5:8000": false, "172.20.0.2:80": false,
		"169.254.169.254:80": false, "100.64.1.1:443": false, "[::1]:443": false, "[fd00::1]:443": false,
		"[fe80::1]:443": false, "[::ffff:10.0.0.1]:443": false,
	} {
		if err := webhooks.CheckAddress(addr, false); (err == nil) != public {
			t.Errorf("CheckAddress(%s) for a user: %v", addr, err)
		}
		if err := webhooks.CheckAddress(addr, true); err != nil {
			t.Errorf("CheckAddress(%s) for an administrator: %v", addr, err)
		}
	}
	for _, addr := range []string{"0.0.0.0:80", "224.0.0.1:80", "[::]:80"} {
		if webhooks.CheckAddress(addr, true) == nil {
			t.Errorf("CheckAddress(%s) accepted", addr)
		}
	}
}
