package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"filebrowser/internal/config"
)

// static serves the embedded SPA. index.html is returned for client-side
// routes; its inline bootstrap scripts are allowed by hash in the CSP.
type static struct {
	fsys     fs.FS
	index    []byte
	appCSP   string
	shareCSP string
	modTime  time.Time
}

var inlineScript = regexp.MustCompile(`(?is)<script((?:\s[^>]*)?)>(.*?)</script>`)

func newStatic(fsys fs.FS, cfg config.Config) (*static, error) {
	index, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		return nil, err
	}
	var hashes []string
	for _, m := range inlineScript.FindAllSubmatch(index, -1) {
		if bytes.Contains(bytes.ToLower(m[1]), []byte("src=")) {
			continue
		}
		sum := sha256.Sum256(m[2])
		hashes = append(hashes, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
	}
	scriptSrc := strings.TrimSpace("'self' " + strings.Join(hashes, " "))
	common := "default-src 'self'; script-src " + scriptSrc + "; style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' blob: data:; media-src 'self' blob:; font-src 'self'; connect-src 'self'; worker-src 'self'; " +
		"object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
	return &static{
		fsys:     fsys,
		index:    index,
		appCSP:   common + "; frame-src " + cfg.ShareOrigin,
		shareCSP: common + "; frame-src 'self'",
		modTime:  time.Now(),
	}, nil
}

const robots = "User-agent: *\nDisallow: /\n"

func (st *static) serve(w http.ResponseWriter, r *http.Request, share bool) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p := path.Clean("/" + r.URL.Path)
	if p == "/robots.txt" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(robots))
		return
	}
	name := strings.TrimPrefix(p, "/")
	if name != "" && name != "index.html" {
		if f, err := st.fsys.Open(name); err == nil {
			defer f.Close()
			if fi, err := f.Stat(); err == nil && !fi.IsDir() {
				if strings.HasPrefix(name, "_app/immutable/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					w.Header().Set("Cache-Control", "no-cache")
				}
				if rs, ok := f.(interface {
					Read([]byte) (int, error)
					Seek(int64, int) (int64, error)
				}); ok {
					http.ServeContent(w, r, name, st.modTime, rs)
					return
				}
			}
		}
		// Missing asset: do not fall back to the SPA.
		if strings.HasPrefix(name, "_app/") || path.Ext(name) != "" {
			http.NotFound(w, r)
			return
		}
	}
	if share && !strings.HasPrefix(p, "/s/") {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	if share {
		h.Set("Content-Security-Policy", st.shareCSP)
		h.Set("X-Robots-Tag", "noindex, nofollow")
	} else {
		h.Set("Content-Security-Policy", st.appCSP)
	}
	http.ServeContent(w, r, "index.html", st.modTime, bytes.NewReader(st.index))
}
