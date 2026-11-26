package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"html"
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
	fsys fs.FS
	// The shell up to </head> and from it: the meta tags of the page being
	// asked for go in between (see page).
	head, rest []byte
	appCSP     string
	shareCSP   string
	modTime    time.Time
	cfg        config.Config
	// shareCard is what a share link's preview may say (see Server.shareCard).
	shareCard func(r *http.Request) card
}

// card is what a link says about itself to something that does not run the
// page: the preview a chat app shows for a pasted link.
type card struct{ title, description string }

// page is the shell with the meta tags for one request. Nothing is ever
// counted or claimed by serving it: previews are fetched by the chat app's
// servers, before and instead of the person the link was sent to.
func (st *static) page(c card, origin string) []byte {
	esc := html.EscapeString
	var b bytes.Buffer
	b.Write(st.head)
	for _, m := range [][2]string{
		{`name="application-name"`, st.cfg.SiteName},
		{`name="theme-color"`, st.cfg.ThemeColor},
		{`name="description"`, c.description},
		{`property="og:site_name"`, st.cfg.SiteName},
		{`property="og:type"`, "website"},
		{`property="og:title"`, c.title},
		{`property="og:description"`, c.description},
		{`property="og:image"`, origin + "/og.png"},
		{`name="twitter:card"`, "summary"},
	} {
		b.WriteString("<meta " + m[0] + ` content="` + esc(m[1]) + `" />` + "\n")
	}
	b.Write(st.rest)
	return b.Bytes()
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
	head, rest, ok := bytes.Cut(index, []byte("</head>"))
	if !ok {
		return nil, errors.New("index.html has no </head>")
	}
	return &static{
		fsys:     fsys,
		head:     head,
		rest:     append([]byte("</head>"), rest...),
		appCSP:   common + "; frame-src " + cfg.ShareOrigin,
		shareCSP: common + "; frame-src 'self'",
		modTime:  time.Now(),
		cfg:      cfg,
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
	c, origin := card{st.cfg.SiteName, "Sign in to browse and share files."}, st.cfg.AppOrigin
	if share {
		h.Set("Content-Security-Policy", st.shareCSP)
		h.Set("X-Robots-Tag", "noindex, nofollow")
		c, origin = st.shareCard(r), st.cfg.ShareOrigin
	} else {
		h.Set("Content-Security-Policy", st.appCSP)
	}
	// The page differs by link, so it carries no validator to be cached under.
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(st.page(c, origin)))
}
