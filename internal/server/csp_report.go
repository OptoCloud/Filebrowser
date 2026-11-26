package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"filebrowser/internal/httpx"
)

// handleCSPReport logs a browser's report that a page did something its
// Content-Security-Policy forbids (or, for the report-only policy, would
// forbid): a sign of a bug in a page, of an extension, or of an injection
// that the policy stopped. Anyone can send one, so they are few per client,
// small, and only ever logged.
func (s *Server) handleCSPReport(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
	if !s.reportLimiter.Allow(httpx.LimitKey(r.Context()), time.Now()) {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 8<<10))
	if err != nil {
		return
	}
	var body struct {
		Report map[string]any `json:"csp-report"`
	}
	if json.Unmarshal(raw, &body) != nil || body.Report == nil {
		return
	}
	field := func(k string) string {
		v, _ := body.Report[k].(string)
		if len(v) > 300 {
			v = v[:300]
		}
		return v
	}
	slog.Warn("content security policy violation", "host", r.Host, "ip", httpx.IP(r.Context()),
		"directive", field("effective-directive"), "violated", field("violated-directive"),
		"blocked", field("blocked-uri"), "document", httpx.RedactPath(field("document-uri")),
		"source", field("source-file"), "sample", field("script-sample"), "disposition", field("disposition"))
}
