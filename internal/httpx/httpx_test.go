package httpx

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestClientIPTrustsOnlyProxies(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("CF-Connecting-IP", "1.2.3.4")
	r.RemoteAddr = "127.0.0.1:5000"
	if got := ClientIP(r, "CF-Connecting-IP", trusted); got != "1.2.3.4" {
		t.Errorf("from proxy: %s", got)
	}
	r.RemoteAddr = "203.0.113.5:5000"
	if got := ClientIP(r, "CF-Connecting-IP", trusted); got != "203.0.113.5" {
		t.Errorf("spoofed header believed: %s", got)
	}
}

func TestRedactPath(t *testing.T) {
	for in, want := range map[string]string{
		"/s/abc/file": "/s/***/file", "/x/tok/name": "/x/***/name", "/invite/tok": "/invite/***", "/api/files": "/api/files",
	} {
		if got := RedactPath(in); got != want {
			t.Errorf("RedactPath(%q) = %q", in, got)
		}
	}
}
