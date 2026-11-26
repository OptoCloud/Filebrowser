package server

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"filebrowser/internal/config"
	"filebrowser/internal/store"
)

// fakeS3 is the little of S3 the upload buffer uses, in memory. It checks no
// signatures (internal/r2's tests do that against a real server) but holds a
// part to the content-length its address was signed for.
type fakeS3 struct {
	mu      sync.Mutex
	uploads map[string]map[int][]byte // upload id -> part number -> data
	objects map[string][]byte
	next    int
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := r.URL.Query()
	key := strings.TrimPrefix(r.URL.Path, "/bucket/")
	id := q.Get("uploadId")
	switch {
	case r.Method == "POST" && q.Has("uploads"):
		f.next++
		id = "up" + strconv.Itoa(f.next)
		f.uploads[id] = map[int][]byte{}
		fmt.Fprintf(w, "<InitiateMultipartUploadResult><UploadId>%s</UploadId></InitiateMultipartUploadResult>", id)
	case r.Method == "PUT" && id != "":
		body, _ := io.ReadAll(r.Body)
		signed := strings.Contains(q.Get("X-Amz-SignedHeaders"), "content-length")
		if f.uploads[id] == nil || !signed {
			http.Error(w, "no", http.StatusForbidden)
			return
		}
		n, _ := strconv.Atoi(q.Get("partNumber"))
		f.uploads[id][n] = body
		w.Header().Set("ETag", `"`+md5hex(body)+`"`)
	case r.Method == "GET" && id != "":
		if f.uploads[id] == nil {
			http.Error(w, "<Error><Code>NoSuchUpload</Code></Error>", 404)
			return
		}
		var ns []int
		for n := range f.uploads[id] {
			ns = append(ns, n)
		}
		sort.Ints(ns)
		fmt.Fprint(w, "<ListPartsResult>")
		for _, n := range ns {
			fmt.Fprintf(w, `<Part><PartNumber>%d</PartNumber><ETag>"%s"</ETag><Size>%d</Size></Part>`, n, md5hex(f.uploads[id][n]), len(f.uploads[id][n]))
		}
		fmt.Fprint(w, "</ListPartsResult>")
	case r.Method == "POST" && id != "":
		var in struct {
			Parts []struct{ PartNumber int } `xml:"Part"`
		}
		xml.NewDecoder(r.Body).Decode(&in)
		var obj []byte
		for _, p := range in.Parts {
			obj = append(obj, f.uploads[id][p.PartNumber]...)
		}
		f.objects[key] = obj
		delete(f.uploads, id)
		fmt.Fprint(w, "<CompleteMultipartUploadResult/>")
	case r.Method == "DELETE" && id != "":
		delete(f.uploads, id)
		w.WriteHeader(204)
	case r.Method == "DELETE":
		delete(f.objects, key)
		w.WriteHeader(204)
	case r.Method == "GET" || r.Method == "HEAD":
		obj, ok := f.objects[key]
		if !ok {
			http.Error(w, "<Error><Code>NoSuchKey</Code></Error>", 404)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(obj)))
		if r.Method == "GET" {
			w.Write(obj)
		}
	default:
		http.Error(w, "unexpected", 400)
	}
}

func md5hex(b []byte) string { s := md5.Sum(b); return hex.EncodeToString(s[:]) }

// r2Harness is a server with an upload bucket: a real S3-compatible one if
// FB_TEST_S3_ENDPOINT is set (see internal/r2), else the fake.
func r2Harness(t *testing.T) (*harness, *fakeS3) {
	var fake *fakeS3
	r2cfg := config.R2{Region: "auto", Bucket: "bucket", AccessKeyID: "k", SecretAccessKey: "s",
		Threshold: 1, PartSize: 5 << 20, MaxPendingBytes: 64 << 20, VerifyMD5: true}
	if ep := os.Getenv("FB_TEST_S3_ENDPOINT"); ep != "" {
		r2cfg.Endpoint, r2cfg.Bucket = ep, os.Getenv("FB_TEST_S3_BUCKET")
		r2cfg.AccessKeyID, r2cfg.SecretAccessKey, r2cfg.Region = os.Getenv("FB_TEST_S3_KEY"), os.Getenv("FB_TEST_S3_SECRET"), os.Getenv("FB_TEST_S3_REGION")
	} else {
		fake = &fakeS3{uploads: map[string]map[int][]byte{}, objects: map[string][]byte{}}
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)
		r2cfg.Endpoint = srv.URL
	}
	h := newHarnessWith(t, func(c *config.Config) { c.R2 = r2cfg })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go h.srv.r2.Run(ctx)
	return h, fake
}

// r2Send uploads data the way the page does: begin, PUT each part to its
// signed address, complete, and wait for the server to have pulled it.
func r2Send(t *testing.T, c *client, prefix string, begin map[string]any, data []byte) (map[string]any, int) {
	t.Helper()
	begin["size"] = len(data)
	rec := c.do("POST", prefix+"begin", begin)
	if rec.Code != 200 {
		return decode[map[string]any](t, rec), rec.Code
	}
	b := decode[map[string]any](t, rec)
	id, partSize, n := b["id"].(string), int(b["part_size"].(float64)), int(b["parts"].(float64))
	var want []int
	for i := 1; i <= n; i++ {
		want = append(want, i)
	}
	urls := decode[map[string]map[string]string](t, c.do("POST", prefix+"sign", map[string]any{"id": id, "parts": want}))["urls"]
	for i := 1; i <= n; i++ {
		part := data[(i-1)*partSize : min(i*partSize, len(data))]
		req, _ := http.NewRequest("PUT", urls[strconv.Itoa(i)], bytes.NewReader(part))
		res, err := http.DefaultClient.Do(req)
		if err != nil || res.StatusCode != 200 {
			t.Fatalf("part %d: %v %v", i, res, err)
		}
		res.Body.Close()
	}
	rec = c.do("POST", prefix+"complete", map[string]any{"id": id})
	if rec.Code != 200 {
		return decode[map[string]any](t, rec), rec.Code
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		st := decode[map[string]any](t, c.do("GET", prefix+"status?id="+id, nil))
		if s := st["state"]; s == "done" || s == "failed" || s == "expired" {
			return st, 200
		}
		if time.Now().After(deadline) {
			t.Fatalf("upload still %v", st)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestR2Uploads(t *testing.T) {
	h, fake := r2Harness(t)
	u := h.user("rosa", "user", store.Grant{Path: "rosa", Permission: "write"}, store.Grant{Path: "ro", Permission: "read"})
	os.MkdirAll(filepath.Join(h.root, "rosa", "inbox"), 0o755)
	os.MkdirAll(filepath.Join(h.root, "ro"), 0o755)
	c := h.session(u, time.Minute)
	data := bytes.Repeat([]byte("filebrowser-r2-"), (5<<20+4321)/15+1)[:5<<20+4321]

	// A file in two parts arrives whole, and leaves the bucket.
	st, code := r2Send(t, c, "/api/r2/", map[string]any{"dir": "rosa", "name": "video.bin"}, data)
	if code != 200 || st["state"] != "done" || st["path"] != "rosa/video.bin" {
		t.Fatalf("upload: %d %v", code, st)
	}
	if got, _ := os.ReadFile(filepath.Join(h.root, "rosa/video.bin")); !bytes.Equal(got, data) {
		t.Fatal("the stored file is not what was sent")
	}
	if fake != nil && (len(fake.objects) != 0 || len(fake.uploads) != 0) {
		t.Fatalf("left in the bucket: %d objects, %d uploads", len(fake.objects), len(fake.uploads))
	}
	me := decode[map[string]any](t, c.do("GET", "/api/me", nil))
	if me["r2_threshold"] != float64(1) {
		t.Fatalf("me = %v", me["r2_threshold"])
	}
	// The same name again: the next free one.
	if st, _ := r2Send(t, c, "/api/r2/", map[string]any{"dir": "rosa", "name": "video.bin"}, data[:100]); st["name"] != "video (1).bin" {
		t.Fatalf("second upload: %v", st)
	}

	// Refused before anything is sent: no write access, a bad name, the quota.
	for _, b := range []map[string]any{
		{"dir": "ro", "name": "x.bin"}, {"dir": "elsewhere", "name": "x.bin"}, {"dir": "rosa", "name": "../x.bin"}, {"dir": "rosa", "name": "a:b"},
	} {
		if _, code := r2Send(t, c, "/api/r2/", b, data[:10]); code < 400 {
			t.Errorf("begin %v: %d", b, code)
		}
	}
	q := int64(len(data) + 200)
	h.store.UpdateUser(context.Background(), u.ID, store.UserUpdate{QuotaBytes: &q})
	if _, code := r2Send(t, c, "/api/r2/", map[string]any{"dir": "rosa", "name": "big.bin"}, data); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over quota: %d", code)
	}
	q = 0
	h.store.UpdateUser(context.Background(), u.ID, store.UserUpdate{QuotaBytes: &q})

	// Completing with a part missing is refused and says which; another
	// user cannot touch the upload; aborting gives everything back.
	rec := c.do("POST", "/api/r2/begin", map[string]any{"dir": "rosa", "name": "partial.bin", "size": len(data)})
	expect(t, rec, 200)
	id := decode[map[string]any](t, rec)["id"].(string)
	rec = c.do("POST", "/api/r2/complete", map[string]any{"id": id})
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "parts_missing") {
		t.Fatalf("complete without parts: %d %s", rec.Code, rec.Body.String())
	}
	other := h.session(h.user("sam", "user", store.Grant{Path: "sam", Permission: "write"}), time.Minute)
	expect(t, other.do("POST", "/api/r2/sign", map[string]any{"id": id, "parts": []int{1}}), 404)
	expect(t, other.do("GET", "/api/r2/status?id="+id, nil), 404)
	expect(t, c.do("POST", "/api/r2/sign", map[string]any{"id": id, "parts": []int{3}}), 400)
	if have := decode[map[string]any](t, c.do("GET", "/api/r2/status?id="+id, nil)); have["state"] != "uploading" || len(have["have"].([]any)) != 0 {
		t.Fatalf("status = %v", have)
	}
	expect(t, c.do("POST", "/api/r2/abort", map[string]any{"id": id}), 200)
	expect(t, c.do("POST", "/api/r2/sign", map[string]any{"id": id, "parts": []int{1}}), 409)
	if fake != nil && len(fake.uploads) != 0 {
		t.Fatal("the aborted upload is still in the bucket")
	}

	// A drop link: its limits hold, by size before and by content after.
	created := decode[map[string]any](t, c.do("POST", "/api/shares", map[string]any{
		"path": "rosa/inbox", "kind": "upload", "max_bytes": 6 << 20, "allowed_types": []string{"image"}, "ask_name": true}))
	s := h.share()
	s.headers["X-Share-Token"] = strings.TrimPrefix(created["url"].(string), shareOrigin+"/s/")
	png := append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), bytes.Repeat([]byte{0}, 2000)...)
	if _, code := r2Send(t, s, "/_r2/", map[string]any{"name": "a.png"}, png); code != 400 {
		t.Fatalf("drop without a name: %d", code)
	}
	if _, code := r2Send(t, s, "/_r2/", map[string]any{"name": "notes.txt", "uploader": "Jo"}, png); code != http.StatusUnsupportedMediaType {
		t.Fatalf("drop of the wrong kind: %d", code)
	}
	st, _ = r2Send(t, s, "/_r2/", map[string]any{"name": "fake.png", "uploader": "Jo"}, []byte("<script>alert(1)</script>"))
	if st["state"] != "failed" || st["error"] == "" {
		t.Fatalf("a script named .png: %v", st)
	}
	st, _ = r2Send(t, s, "/_r2/", map[string]any{"name": "real.png", "uploader": "Jo"}, png)
	if st["state"] != "done" || st["name"] != "real.png" || st["path"] != nil {
		t.Fatalf("drop: %v", st)
	}
	if _, err := os.Stat(filepath.Join(h.root, "rosa/inbox/Jo/real.png")); err != nil {
		t.Fatal(err)
	}
	// The link's total: 6 MiB, of which 5 MiB fit once and not twice.
	big := append(bytes.Clone(png), bytes.Repeat([]byte{0}, 5<<20-len(png))...)
	if st, _ := r2Send(t, s, "/_r2/", map[string]any{"name": "big.png", "uploader": "Jo"}, big); st["state"] != "done" {
		t.Fatalf("5 MiB into a 6 MiB link: %v", st)
	}
	if _, code := r2Send(t, s, "/_r2/", map[string]any{"name": "more.png", "uploader": "Jo"}, big); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("past the link's total: %d", code)
	}
}
